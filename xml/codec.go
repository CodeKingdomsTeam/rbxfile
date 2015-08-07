package xml

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/robloxapi/rbxdump"
	"github.com/robloxapi/rbxfile"
	"github.com/satori/go.uuid"
	"io"
	"io/ioutil"
	"sort"
	"strconv"
	"strings"
)

type Mode uint8

const (
	ModePlace Mode = iota // Data is decoded and encoded as a Roblox place (RBXL) file.
	ModeModel             // Data is decoded and encoded as a Roblox model (RBXM) file.
)

// RobloxCodec implements Decoder and Encoder to emulate Roblox's internal
// codec as closely as possible.
type RobloxCodec struct {
	Mode Mode
}

type propRef struct {
	inst *rbxfile.Instance
	prop string
	ref  string
}

func (c RobloxCodec) Decode(document *Document, api *rbxdump.API) (root *rbxfile.Root, err error) {
	if document == nil {
		return nil, fmt.Errorf("document is nil")
	}

	dec := &rdecoder{
		document:   document,
		api:        api,
		root:       new(rbxfile.Root),
		instLookup: make(map[string]*rbxfile.Instance),
	}

	dec.decode()
	return dec.root, dec.err
}

type rdecoder struct {
	document   *Document
	api        *rbxdump.API
	root       *rbxfile.Root
	err        error
	instLookup map[string]*rbxfile.Instance
	propRefs   []propRef
}

func (dec *rdecoder) decode() error {
	if dec.err != nil {
		return dec.err
	}

	dec.root = new(rbxfile.Root)
	dec.root.Instances, _ = dec.getItems(nil, dec.document.Root.Tags, nil)

	for _, propRef := range dec.propRefs {
		referent, ok := dec.instLookup[propRef.ref]
		if !ok {
			continue
		}
		propRef.inst.Properties[propRef.prop] = rbxfile.ValueReference{Instance: referent}
	}

	return nil
}

func (dec *rdecoder) getItems(parent *rbxfile.Instance, tags []*Tag, classMembers map[string]*rbxdump.Property) (instances []*rbxfile.Instance, properties map[string]rbxfile.Value) {
	properties = make(map[string]rbxfile.Value)
	hasProps := false

	for _, tag := range tags {
		switch tag.StartName {
		case "Item":
			className, ok := tag.AttrValue("class")
			if !ok {
				// WARN: item with missing class attribute
				continue
			}

			var classMemb map[string]*rbxdump.Property
			if dec.api != nil {
				class := dec.api.Classes[className]
				if class == nil {
					//WARN: invalid class name
				} else {
					for _, member := range class.Members {
						if member, ok := member.(*rbxdump.Property); ok {
							classMemb[member.Name] = member
						}
					}
				}
			}

			instance := rbxfile.NewInstance(className, nil)
			referent, ok := tag.AttrValue("referent")
			if ok && len(referent) > 0 {
				instance.Reference = []byte(referent)
				if !isEmptyRef(referent) {
					dec.instLookup[referent] = instance
				}
			}

			var children []*rbxfile.Instance
			children, instance.Properties = dec.getItems(instance, tag.Tags, classMemb)
			for _, child := range children {
				child.SetParent(instance)
			}

			instances = append(instances, instance)

		case "Properties":
			if hasProps || parent == nil {
				continue
			}
			hasProps = true

			for _, property := range tag.Tags {
				name, value, ok := dec.getProperty(property, parent, classMembers)
				if ok {
					properties[name] = value
				}
			}
		}
	}

	return instances, properties
}

func isEmptyRef(ref string) bool {
	switch ref {
	case "", "null", "nil":
		// A "true" implementation might determine these values from
		// <External> tags.
		return true
	default:
		return false
	}
}

func (dec *rdecoder) getProperty(tag *Tag, instance *rbxfile.Instance, classMembers map[string]*rbxdump.Property) (name string, value rbxfile.Value, ok bool) {
	name, ok = tag.AttrValue("name")
	if !ok {
		return "", nil, false
	}

	var valueType string
	var enum *rbxdump.Enum
	if dec.api != nil && classMembers != nil {
		// Determine property type from API.
		propAPI, ok := classMembers[name]
		if ok {
			valueType = propAPI.ValueType
			if e, ok := dec.api.Enums[valueType]; ok {
				valueType = "token"
				enum = e
			}
			goto processValue
		}
	}

	// Guess property type from tag name
	valueType = dec.getCanonType(tag.StartName)

processValue:
	value, ok = dec.getValue(tag, valueType, enum)
	if !ok {
		return "", nil, false
	}

	ref := getContent(tag)
	if _, ok := value.(rbxfile.ValueReference); ok && !isEmptyRef(ref) {
		dec.propRefs = append(dec.propRefs, propRef{
			inst: instance,
			prop: name,
			ref:  ref,
		})
		return "", nil, false
	}

	return name, value, ok
}

// Converts a string (usually from a tag name) to a decodable type.
func (dec *rdecoder) getCanonType(valueType string) string {
	switch strings.ToLower(valueType) {
	case "axes":
		return "Axes"
	case "binarystring":
		return "BinaryString"
	case "bool":
		return "bool"
	case "brickcolor":
		return "BrickColor"
	case "cframe", "coordinateframe":
		return "CoordinateFrame"
	case "color3":
		return "Color3"
	case "content":
		return "Content"
	case "double":
		return "double"
	case "faces":
		return "Faces"
	case "float":
		return "float"
	case "int":
		return "int"
	case "protectedstring":
		return "ProtectedString"
	case "ray":
		return "Ray"
	case "object", "ref":
		return "Object"
	case "string":
		return "string"
	case "token":
		return "token"
	case "udim":
		return "UDim"
	case "udim2":
		return "UDim2"
	case "vector2":
		return "Vector2"
	case "vector2int16":
		return "Vector2int16"
	case "vector3":
		return "Vector3"
	case "vector3int16":
		return "Vector3int16"
	}
	return ""
}

// Gets a rbxfile.Value from a property tag, using valueType to determine how
// the tag is interpreted. valueType must be an existing type as it appears in
// the API dump. If guessing the type, it should be converted to one of these
// first.
func (dec *rdecoder) getValue(tag *Tag, valueType string, enum *rbxdump.Enum) (value rbxfile.Value, ok bool) {
	switch valueType {
	case "Axes":
		var bits int32
		components{
			"axes": &bits,
		}.getFrom(tag)

		return rbxfile.ValueAxes{
			X: bits&(1<<0) > 0,
			Y: bits&(1<<1) > 0,
			Z: bits&(1<<2) > 0,
		}, true

	case "BinaryString":
		dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(getContent(tag)))
		v, err := ioutil.ReadAll(dec)
		if err != nil {
			return nil, false
		}
		return rbxfile.ValueBinaryString(v), true

	case "bool":
		switch getContent(tag) {
		case "false", "False", "FALSE":
			return rbxfile.ValueBool(false), true
		case "true", "True", "TRUE":
			return rbxfile.ValueBool(true), true
		default:
			return nil, false
		}

	case "BrickColor":
		v, err := strconv.ParseUint(getContent(tag), 10, 32)
		if err != nil {
			return nil, false
		}
		return rbxfile.ValueBrickColor(v), true

	case "CoordinateFrame":
		v := *new(rbxfile.ValueCFrame)
		components{
			"X":   &v.Position.X,
			"Y":   &v.Position.Y,
			"Z":   &v.Position.Z,
			"R00": &v.Rotation[0],
			"R01": &v.Rotation[1],
			"R02": &v.Rotation[2],
			"R10": &v.Rotation[3],
			"R11": &v.Rotation[4],
			"R12": &v.Rotation[5],
			"R20": &v.Rotation[6],
			"R21": &v.Rotation[7],
			"R22": &v.Rotation[8],
		}.getFrom(tag)
		return v, true

	case "Color3":
		if len(tag.Tags) == 0 {
			v, err := strconv.ParseUint(getContent(tag), 10, 32)
			if err != nil {
				return nil, false
			}
			return rbxfile.ValueColor3{
				R: float32(v&0x00FF0000>>16) / 255,
				G: float32(v&0x0000FF00>>8) / 255,
				B: float32(v&0x000000FF) / 255,
			}, true
		} else {
			v := *new(rbxfile.ValueColor3)
			components{
				"R": &v.R,
				"G": &v.G,
				"B": &v.B,
			}.getFrom(tag)
			return v, true
		}

	case "Content":
		if tag.CData == nil && len(tag.Text) > 0 || tag.CData != nil && len(tag.CData) > 0 {
			// Succeeds if CData is not nil but empty, even if Text is not
			// empty. This is correct according to Roblox's codec.
			return nil, false
		}

		for _, subtag := range tag.Tags {
			switch subtag.StartName {
			case "binary":
				//DIFF: Throws `not reading binary data` warning
				fallthrough
			case "hash":
				// Ignored.
				fallthrough
			case "null":
				//DIFF: If null tag has content, then `tag expected` error is
				//thrown.
				return rbxfile.ValueContent{}, true
			case "url":
				return rbxfile.ValueContent(getContent(subtag)), true
			default:
				//DIFF: Throws error `TextXmlParser::parse - Unknown tag ''.`
				return nil, false
			}
		}

		// Tag has no subtags.

		//DIFF: Attempts to read end tag as a subtag, erroneously throwing an
		//"unknown tag" error.
		return nil, false

	case "double":
		v, err := strconv.ParseFloat(getContent(tag), 64)
		if err != nil {
			return nil, false
		}
		return rbxfile.ValueDouble(v), true

	case "Faces":
		var bits int32
		components{
			"faces": &bits,
		}.getFrom(tag)

		return rbxfile.ValueFaces{
			Right:  bits&(1<<0) > 0,
			Top:    bits&(1<<1) > 0,
			Back:   bits&(1<<2) > 0,
			Left:   bits&(1<<3) > 0,
			Bottom: bits&(1<<4) > 0,
			Front:  bits&(1<<5) > 0,
		}, true

	case "float":
		v, err := strconv.ParseFloat(getContent(tag), 32)
		if err != nil {
			return nil, false
		}
		return rbxfile.ValueFloat(v), true

	case "int":
		v, err := strconv.ParseInt(getContent(tag), 10, 32)
		if err != nil {
			return nil, false
		}
		return rbxfile.ValueInt(v), true

	case "ProtectedString":
		return rbxfile.ValueProtectedString(getContent(tag)), true

	case "Ray":
		var origin, direction *Tag
		components{
			"origin":    &origin,
			"direction": &direction,
		}.getFrom(tag)

		v := *new(rbxfile.ValueRay)

		components{
			"X": &v.Origin.X,
			"Y": &v.Origin.Y,
			"Z": &v.Origin.Z,
		}.getFrom(origin)

		components{
			"X": &v.Direction.X,
			"Y": &v.Direction.Y,
			"Z": &v.Direction.Z,
		}.getFrom(direction)

		return v, true

	case "Object":
		// Return empty ValueReference; this signals that the value will be
		// acquired later.
		return rbxfile.ValueReference{}, true

	case "string":
		return rbxfile.ValueString(getContent(tag)), true

	case "token":
		v, err := strconv.ParseUint(getContent(tag), 10, 32)
		if err != nil {
			return nil, false
		}
		if enum != nil {
			// Verify that value is a valid enum item
			for _, item := range enum.Items {
				if uint32(v) == item.Value {
					return rbxfile.ValueToken(v), true
				}
			}
			return rbxfile.ValueToken(v), false
		} else {
			// Assume that it is correct
			return rbxfile.ValueToken(v), true
		}

	case "UDim":
		// Unknown
		return nil, false

	case "UDim2":
		// DIFF: UDim2 is initialized with odd values
		v := *new(rbxfile.ValueUDim2)
		components{
			"XS": &v.X.Scale,
			"XO": &v.X.Offset,
			"YS": &v.Y.Scale,
			"YO": &v.Y.Offset,
		}.getFrom(tag)
		return v, true

	case "Vector2":
		// DIFF: If any component tags are missing, entire value fails
		v := *new(rbxfile.ValueVector2)
		components{
			"X": &v.X,
			"Y": &v.Y,
		}.getFrom(tag)
		return v, true

	case "Vector2int16":
		// Unknown; guessed
		v := *new(rbxfile.ValueVector2int16)
		components{
			"X": &v.X,
			"Y": &v.Y,
		}.getFrom(tag)
		return v, true

	case "Vector3":
		v := *new(rbxfile.ValueVector3)
		components{
			"X": &v.X,
			"Y": &v.Y,
			"Z": &v.Z,
		}.getFrom(tag)
		return v, true

	case "Vector3int16":
		// Unknown; guessed
		v := *new(rbxfile.ValueVector3int16)
		components{
			"X": &v.X,
			"Y": &v.Y,
			"Z": &v.Z,
		}.getFrom(tag)
		return v, true
	}

	return nil, false
}

type components map[string]interface{}

func (c components) getFrom(tag *Tag) {
	// Used to ensure that only the first matched tag is selected.
	d := map[string]bool{}

	for _, subtag := range tag.Tags {
		if p, ok := c[subtag.StartName]; ok && !d[subtag.StartName] {
			d[subtag.StartName] = true
			switch v := p.(type) {
			case *int16:
				if n, err := strconv.ParseInt(getContent(subtag), 10, 16); err == nil {
					*v = int16(n)
				}
			case *int32:
				if n, err := strconv.ParseInt(getContent(subtag), 10, 32); err == nil {
					*v = int32(n)
				}
			case *float32:
				if n, err := strconv.ParseFloat(getContent(subtag), 32); err == nil {
					*v = float32(n)
				}
			case **Tag:
				*v = subtag
			}
		}
	}
}

// Reads either the CData or the text of a tag.
func getContent(tag *Tag) string {
	if tag.CData != nil {
		// CData is preferred even if it is empty
		return string(tag.CData)
	}
	return tag.Text
}

type rencoder struct {
	root     *rbxfile.Root
	api      *rbxdump.API
	document *Document
	refs     map[string]*rbxfile.Instance
	err      error
}

func (c RobloxCodec) Encode(root *rbxfile.Root, api *rbxdump.API) (document *Document, err error) {
	enc := &rencoder{
		root: root,
		api:  api,
		refs: make(map[string]*rbxfile.Instance),
	}

	enc.encode()
	return enc.document, enc.err

}

func (enc *rencoder) encode() {
	enc.document = &Document{
		Prefix: "",
		Indent: "\t",
		Suffix: "",
		Root: NewRoot(
			&Tag{
				StartName: "External",
				Text:      "null",
			},
			&Tag{
				StartName: "External",
				Text:      "nil",
			},
		),
	}

	for _, instance := range enc.root.Instances {
		enc.encodeInstance(instance, enc.document.Root)
	}

}

func (enc *rencoder) encodeInstance(instance *rbxfile.Instance, parent *Tag) {
	if enc.api != nil {
		if _, ok := enc.api.Classes[instance.ClassName]; !ok {
			//WARN: `ClassName` is not a valid class
			return
		}
	}

	ref := enc.checkRef(instance)
	properties := enc.encodeProperties(instance)
	item := NewItem(instance.ClassName, ref, properties...)
	parent.Tags = append(parent.Tags, item)

	for _, child := range instance.GetChildren() {
		enc.encodeInstance(child, item)
	}
}

func (enc *rencoder) encodeProperties(instance *rbxfile.Instance) (properties []*Tag) {
	var apiMembers map[string]*rbxdump.Property
	if enc.api != nil {
		apiClass, ok := enc.api.Classes[instance.ClassName]
		if ok {
			apiMembers = make(map[string]*rbxdump.Property, len(apiClass.Members))
			for _, member := range apiClass.Members {
				if member, ok := member.(*rbxdump.Property); ok {
					apiMembers[member.Name] = member
				}
			}
		}
	}

	// Sort properties by name
	sorted := make([]string, 0, len(instance.Properties))
	for name := range instance.Properties {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)

	for _, name := range sorted {
		value := instance.Properties[name]
		if apiMembers != nil {
			apiMember, ok := apiMembers[name]
			if ok {
				typ := apiMember.ValueType
				token, istoken := value.(rbxfile.ValueToken)
				enum := enc.api.Enums[typ]
				if istoken && enum == nil || !isCanonType(typ, value) {
					//WARN: incorrect value type for property
					continue
				} else if istoken && enum != nil {
					for _, item := range enum.Items {
						if uint32(token) == item.Value {
							goto finishToken
						}
					}

					//WARN: invalid value for token
					continue

				finishToken:
				}
			}
		}

		tag := enc.encodeProperty(instance.ClassName, name, value)
		if tag != nil {
			properties = append(properties, tag)
		}
	}

	return properties
}

func (enc *rencoder) encodeProperty(class, prop string, value rbxfile.Value) *Tag {
	attr := []Attr{Attr{Name: "name", Value: prop}}
	switch value := value.(type) {
	case rbxfile.ValueAxes:
		var n uint64
		for i, b := range []bool{value.X, value.Y, value.Z} {
			if b {
				n |= (1 << uint(i))
			}
		}
		return &Tag{
			StartName: "Axes",
			Attr:      attr,
			Tags: []*Tag{
				&Tag{
					StartName: "axes",
					NoIndent:  true,
					Text:      strconv.FormatUint(n, 10),
				},
			},
		}

	case rbxfile.ValueBinaryString:
		buf := new(bytes.Buffer)
		sw := &lineSplit{w: buf, s: 72, n: 72}
		bw := base64.NewEncoder(base64.StdEncoding, sw)
		bw.Write([]byte(value))
		bw.Close()
		tag := &Tag{
			StartName: "BinaryString",
			Attr:      attr,
			NoIndent:  true,
		}
		encodeContent(tag, buf.String())
		return tag

	case rbxfile.ValueBool:
		var v string
		if value {
			v = "true"
		} else {
			v = "false"
		}
		return &Tag{
			StartName: "bool",
			Attr:      attr,
			NoIndent:  true,
			Text:      v,
		}

	case rbxfile.ValueBrickColor:
		return &Tag{
			StartName: "int",
			Attr:      attr,
			NoIndent:  true,
			Text:      strconv.FormatUint(uint64(value), 10),
		}

	case rbxfile.ValueCFrame:
		return &Tag{
			StartName: "CoordinateFrame",
			Attr:      attr,
			Tags: []*Tag{
				&Tag{StartName: "X", NoIndent: true, Text: encodeFloat(value.Position.X)},
				&Tag{StartName: "Y", NoIndent: true, Text: encodeFloat(value.Position.Y)},
				&Tag{StartName: "Z", NoIndent: true, Text: encodeFloat(value.Position.Z)},
				&Tag{StartName: "R00", NoIndent: true, Text: encodeFloat(value.Rotation[0])},
				&Tag{StartName: "R01", NoIndent: true, Text: encodeFloat(value.Rotation[1])},
				&Tag{StartName: "R02", NoIndent: true, Text: encodeFloat(value.Rotation[2])},
				&Tag{StartName: "R10", NoIndent: true, Text: encodeFloat(value.Rotation[3])},
				&Tag{StartName: "R11", NoIndent: true, Text: encodeFloat(value.Rotation[4])},
				&Tag{StartName: "R12", NoIndent: true, Text: encodeFloat(value.Rotation[5])},
				&Tag{StartName: "R20", NoIndent: true, Text: encodeFloat(value.Rotation[6])},
				&Tag{StartName: "R21", NoIndent: true, Text: encodeFloat(value.Rotation[7])},
				&Tag{StartName: "R22", NoIndent: true, Text: encodeFloat(value.Rotation[8])},
			},
		}

	case rbxfile.ValueColor3:
		r := uint64(value.R * 255)
		g := uint64(value.G * 255)
		b := uint64(value.B * 255)
		return &Tag{
			StartName: "Color3",
			Attr:      attr,
			NoIndent:  true,
			Text:      strconv.FormatUint(0xFF<<24|r<<16|g<<8|b, 10),
		}

	case rbxfile.ValueContent:
		tag := &Tag{
			StartName: "Content",
			Attr:      attr,
			NoIndent:  true,
			Tags: []*Tag{
				&Tag{
					StartName: "",
					NoIndent:  true,
				},
			},
		}
		if len(value) == 0 {
			tag.Tags[0].StartName = "null"
		} else {
			tag.Tags[0].StartName = "url"
			tag.Tags[0].Text = string(value)
		}
		return tag

	case rbxfile.ValueDouble:
		return &Tag{
			StartName: "double",
			Attr:      attr,
			NoIndent:  true,
			Text:      encodeDouble(float64(value)),
		}

	case rbxfile.ValueFaces:
		var n uint64
		for i, b := range []bool{value.Right, value.Top, value.Back, value.Left, value.Bottom, value.Front} {
			if b {
				n |= (1 << uint(i))
			}
		}
		return &Tag{
			StartName: "Faces",
			Attr:      attr,
			Tags: []*Tag{
				&Tag{
					StartName: "faces",
					NoIndent:  true,
					Text:      strconv.FormatUint(n, 10),
				},
			},
		}

	case rbxfile.ValueFloat:
		return &Tag{
			StartName: "float",
			Attr:      attr,
			NoIndent:  true,
			Text:      encodeFloat(float32(value)),
		}

	case rbxfile.ValueInt:
		return &Tag{
			StartName: "int",
			Attr:      attr,
			NoIndent:  true,
			Text:      strconv.FormatInt(int64(value), 10),
		}

	case rbxfile.ValueProtectedString:
		tag := &Tag{
			StartName: "ProtectedString",
			Attr:      attr,
			NoIndent:  true,
		}
		encodeContent(tag, string(value))
		return tag

	case rbxfile.ValueRay:
		return &Tag{
			StartName: "Ray",
			Attr:      attr,
			Tags: []*Tag{
				&Tag{
					StartName: "origin",
					Tags: []*Tag{
						&Tag{StartName: "X", NoIndent: true, Text: encodeFloat(value.Origin.X)},
						&Tag{StartName: "Y", NoIndent: true, Text: encodeFloat(value.Origin.Y)},
						&Tag{StartName: "Z", NoIndent: true, Text: encodeFloat(value.Origin.Z)},
					},
				},
				&Tag{
					StartName: "direction",
					Tags: []*Tag{
						&Tag{StartName: "X", NoIndent: true, Text: encodeFloat(value.Origin.X)},
						&Tag{StartName: "Y", NoIndent: true, Text: encodeFloat(value.Origin.Y)},
						&Tag{StartName: "Z", NoIndent: true, Text: encodeFloat(value.Origin.Z)},
					},
				},
			},
		}

	case rbxfile.ValueReference:
		tag := &Tag{
			StartName: "Ref",
			Attr:      attr,
			NoIndent:  true,
		}

		referent := value.Instance
		if referent != nil {
			tag.Text = enc.checkRef(referent)
		} else {
			tag.Text = "null"
		}
		return tag

	case rbxfile.ValueString:
		return &Tag{
			StartName: "string",
			Attr:      attr,
			NoIndent:  true,
			Text:      string(value),
		}

	case rbxfile.ValueToken:
		return &Tag{
			StartName: "token",
			Attr:      attr,
			NoIndent:  true,
			Text:      strconv.FormatUint(uint64(value), 10),
		}

	case rbxfile.ValueUDim:
		return nil

	case rbxfile.ValueUDim2:
		return &Tag{
			StartName: "UDim2",
			Attr:      attr,
			Tags: []*Tag{
				&Tag{StartName: "XS", NoIndent: true, Text: encodeFloat(value.X.Scale)},
				&Tag{StartName: "XO", NoIndent: true, Text: strconv.FormatInt(int64(value.X.Offset), 10)},
				&Tag{StartName: "YS", NoIndent: true, Text: encodeFloat(value.Y.Scale)},
				&Tag{StartName: "YO", NoIndent: true, Text: strconv.FormatInt(int64(value.Y.Offset), 10)},
			},
		}

	case rbxfile.ValueVector2:
		return &Tag{
			StartName: "Vector2",
			Attr:      attr,
			Tags: []*Tag{
				&Tag{StartName: "X", NoIndent: true, Text: encodeFloat(value.X)},
				&Tag{StartName: "Y", NoIndent: true, Text: encodeFloat(value.Y)},
			},
		}

	case rbxfile.ValueVector2int16:
		return &Tag{
			StartName: "Vector2int16",
			Attr:      attr,
			Tags: []*Tag{
				&Tag{StartName: "X", NoIndent: true, Text: strconv.FormatInt(int64(value.X), 10)},
				&Tag{StartName: "Y", NoIndent: true, Text: strconv.FormatInt(int64(value.Y), 10)},
			},
		}

	case rbxfile.ValueVector3:
		return &Tag{
			StartName: "Vector3",
			Attr:      attr,
			Tags: []*Tag{
				&Tag{StartName: "X", NoIndent: true, Text: encodeFloat(value.X)},
				&Tag{StartName: "Y", NoIndent: true, Text: encodeFloat(value.Y)},
				&Tag{StartName: "Z", NoIndent: true, Text: encodeFloat(value.Z)},
			},
		}

	case rbxfile.ValueVector3int16:
		return &Tag{
			StartName: "Vector3int16",
			Attr:      attr,
			Tags: []*Tag{
				&Tag{StartName: "X", NoIndent: true, Text: strconv.FormatInt(int64(value.X), 10)},
				&Tag{StartName: "Y", NoIndent: true, Text: strconv.FormatInt(int64(value.Y), 10)},
				&Tag{StartName: "Z", NoIndent: true, Text: strconv.FormatInt(int64(value.Z), 10)},
			},
		}
	}

	return nil
}

func (enc *rencoder) checkRef(instance *rbxfile.Instance) (ref string) {
	ref = string(instance.Reference)
	// If the reference is not empty, or if the reference is not marked, or
	// the marked reference already refers to the current instance, then do
	// nothing.
	if isEmptyRef(ref) || enc.refs[ref] != nil && enc.refs[ref] != instance {
		// Otherwise, regenerate the reference until it is not a duplicate.
		for {
			// If a generated reference matches a reference that was not yet
			// traversed, then the latter reference will be regenerated, which
			// may not match Roblox's implementation. It is difficult to
			// discern whetehr this is correct because it is extremely
			// unlikely that a duplicate will be generated.
			ref = generateRef()
			if _, ok := enc.refs[ref]; !ok {
				instance.Reference = []byte(ref)
				break
			}
		}
	}
	// Mark reference as taken.
	enc.refs[ref] = instance
	return ref
}

func generateRef() string {
	return "RBX" + strings.ToUpper(hex.EncodeToString(uuid.NewV4().Bytes()))
}

type lineSplit struct {
	w io.Writer
	s int
	n int
}

func (l *lineSplit) Write(p []byte) (n int, err error) {
	for i := 0; ; {
		var q []byte
		if len(p[i:]) < l.n {
			q = p[i:]
		} else {
			q = p[i : i+l.n]
		}
		n, err = l.w.Write(q)
		if n < len(q) {
			return
		}
		l.n -= len(q)
		i += len(q)
		if i >= len(p) {
			break
		}
		if l.n <= 0 {
			_, e := l.w.Write([]byte{'\n'})
			if e != nil {
				return
			}
			l.n = l.s
		}
	}
	return
}

func encodeFloat(f float32) string {
	s := strconv.FormatFloat(float64(f), 'g', 9, 32)
	if e := strings.Index(s, "e"); e >= 0 {
		// Adjust exponent to have length of at least 3, using leading zeros.
		exp := s[e+2:]
		if len(exp) < 3 {
			s = s[:e+2] + strings.Repeat("0", 3-len(exp)) + exp
		}
	}
	return s
}

func encodeDouble(f float64) string {
	return strconv.FormatFloat(f, 'g', 9, 64)
}

func encodeContent(tag *Tag, text string) {
	if len(text) > 0 && strings.Index(text, "]]>") == -1 {
		tag.CData = []byte(text)
		return
	}
	tag.Text = text
}

func isCanonType(t string, v rbxfile.Value) bool {
	switch v.(type) {
	case rbxfile.ValueAxes:
		return t == "Axes"
	case rbxfile.ValueBinaryString:
		return t == "BinaryString"
	case rbxfile.ValueBool:
		return t == "bool"
	case rbxfile.ValueBrickColor:
		return t == "BrickColor"
	case rbxfile.ValueCFrame:
		return t == "CoordinateFrame"
	case rbxfile.ValueColor3:
		return t == "Color3"
	case rbxfile.ValueContent:
		return t == "Content"
	case rbxfile.ValueDouble:
		return t == "double"
	case rbxfile.ValueFaces:
		return t == "Faces"
	case rbxfile.ValueFloat:
		return t == "float"
	case rbxfile.ValueInt:
		return t == "int"
	case rbxfile.ValueProtectedString:
		return t == "ProtectedString"
	case rbxfile.ValueRay:
		return t == "Ray"
	case rbxfile.ValueReference:
		return t == "Object"
	case rbxfile.ValueString:
		return t == "string"
	case rbxfile.ValueUDim:
		return t == "UDim"
	case rbxfile.ValueUDim2:
		return t == "UDim2"
	case rbxfile.ValueVector2:
		return t == "Vector2"
	case rbxfile.ValueVector2int16:
		return t == "Vector2int16"
	case rbxfile.ValueVector3:
		return t == "Vector3"
	case rbxfile.ValueVector3int16:
		return t == "Vector3int16"
	}
	return false
}