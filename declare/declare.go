package declare

import (
	"github.com/robloxapi/rbxfile"
)

// Root declares a rbxfile.Root. It is a list that contains Instance
// declarations.
type Root []instance

func build(dinst instance, refs map[string]*rbxfile.Instance, props map[*rbxfile.Instance][]property) *rbxfile.Instance {
	inst := rbxfile.NewInstance(dinst.className, nil)

	if dinst.reference != "" {
		refs[dinst.reference] = inst
		inst.Reference = []byte(dinst.reference)
	}

	inst.Properties = make(map[string]rbxfile.Value, len(dinst.properties))
	props[inst] = dinst.properties

	for _, dchild := range dinst.children {
		child := build(dchild, refs, props)
		child.SetParent(inst)
	}

	return inst
}

// Declare evaluates the Root declaration, generating instances and property
// values, setting up the instance hierarchy, and resolving references.
func (droot Root) Declare() *rbxfile.Root {
	root := &rbxfile.Root{
		Instances: make([]*rbxfile.Instance, 0, len(droot)),
	}

	refs := map[string]*rbxfile.Instance{}
	props := map[*rbxfile.Instance][]property{}

	for _, dinst := range droot {
		root.Instances = append(root.Instances, build(dinst, refs, props))
	}

	for inst, properties := range props {
		for _, prop := range properties {
			inst.Properties[prop.name] = prop.typ.value(refs, prop.value)
		}
	}

	return root
}

type element interface {
	element()
}

type instance struct {
	className  string
	reference  string
	properties []property
	children   []instance
}

func (instance) element() {}

// Declare evaluates the Instance declaration, generating the instance,
// descendants, and property values, setting up the instance hierarchy, and
// resolving references.
func (dinst instance) Declare() *rbxfile.Instance {
	inst := rbxfile.NewInstance(dinst.className, nil)

	refs := map[string]*rbxfile.Instance{}
	props := map[*rbxfile.Instance][]property{}

	if dinst.reference != "" {
		refs[dinst.reference] = inst
		inst.Reference = []byte(dinst.reference)
	}

	inst.Properties = make(map[string]rbxfile.Value, len(dinst.properties))
	props[inst] = dinst.properties

	for _, dchild := range dinst.children {
		child := build(dchild, refs, props)
		child.SetParent(inst)
	}

	for inst, properties := range props {
		for _, prop := range properties {
			inst.Properties[prop.name] = prop.typ.value(refs, prop.value)
		}
	}

	return inst
}

// Instance declares a rbxfile.Instance. It defines an instance with a class
// name, and a series of "elements". An element can be a Property declaration,
// which defines a property for the instance. An element can also be another
// Instance declaration, which becomes a child of the instance.
//
// An element can also be a "Ref" declaration, which defines a string that can
// be used to refer to the instance by properties with the Reference value
// type. This also sets the instance's Reference field.
func Instance(className string, elements ...element) instance {
	inst := instance{
		className: className,
	}

	for _, e := range elements {
		switch e := e.(type) {
		case Ref:
			inst.reference = string(e)
		case property:
			inst.properties = append(inst.properties, e)
		case instance:
			inst.children = append(inst.children, e)
		}
	}

	return inst
}

type property struct {
	name  string
	typ   Type
	value []interface{}
}

func (property) element() {}

// Property declares a property of a rbxfile.Instance. It defines the name of
// the property, a type corresponding to a rbxfile.Value, and the value of the
// property.
//
// The value may be one or more values of any type, which are asserted to a
// rbxfile.Value corresponding to the given type. If the value(s) cannot be
// asserted, then the zero value is returned instead.
//
// When the type or field of the type is a number, any number type except for
// complex numbers may be given as the value.
//
// The value may be a single rbxfile.Value that corresponds to the given type
// (e.g. rbxfile.ValueString for String), in which case the value itself is
// returned.
//
// For a given type, values must be the following:
//
// String, BinaryString, ProtectedString, Content: A single string or []byte.
// Extra values are ignored.
//
// Bool: A single bool. Extra values are ignored.
//
// Int, Float, Double, BrickColor, Token: A single number. Extra values are
// ignored.
//
// UDim: 2 numbers, corresponding to the Scale and Offset fields.
//
// UDim2: 4 numbers, corresponding to the fields X.Scale, X.Offset, Y.Scale,
// and Y.Offset.
//
// Ray: Either 2 rbxfile.ValueVector3s, or 6 numbers. The ValueVector3s
// correspond to the Origin and Direction fields. Each number corresponds to
// the X, Y, and Z fields of Origin, then Direction.
//
// Faces: 6 bools, corresponding to the fields Right, Top, Back, Left, Bottom,
// Front.
//
// Axes: 3 bools, corresponding to the fields X, Y, and Z.
//
// Color3: 3 numbers, corresponding to the fields R, G, and B.
//
// Vector2, Vector2int16: 2 numbers, corresponding to the fields X and Y.
//
// Vector3, Vector3int16: 3 numbers, corresponding to the fields X, Y, and Z.
//
// CFrame: Either 10 or 12 values. When 10 values are given, the first value
// must be a rbxfile.ValueVector3, which corresponds to the Position field.
// The remaining 9 values must be numbers, which correspond to the components
// of the Rotation field. When 12 values are given, all must be numbers, with
// the first 3 corresponding to the X, Y, and Z fields of the Position field.
// The remaining 9 numbers correspond to the Rotation field.
//
// Reference: A single string, []byte or *rbxfile.Instance. Extra values are
// ignored. When the value is a string or []byte, the reference is resolved by
// looking for an instance whose "Ref" declaration is equal to the value.
func Property(name string, typ Type, value ...interface{}) property {
	return property{name: name, typ: typ, value: value}
}

// Declare evaluates the Property declaration. Since the property does not
// belong to any instance, the name is ignored, and only the value is
// generated.
func (prop property) Declare() rbxfile.Value {
	var refs map[string]*rbxfile.Instance
	return prop.typ.value(refs, prop.value)
}

// Ref declares a string that can be used to refer to the Instance under which
// it was declared. This will also set the instance's Reference field.
type Ref string

func (Ref) element() {}