package clispec

// paramKind identifies the value type of a flag.
type paramKind uint8

const (
	kindString paramKind = iota
	kindInt
	kindBool
)

// Param declares a flag and a function that copies its parsed value into
// the input struct after Cobra parses the command line. Each value type
// uses its own function signature without reflection or Go's any type.
type Param[I Input] struct {
	Kind        paramKind
	Flag        string
	Description string
	Required    bool `exhaustruct:"optional"`

	DefaultStr  string `exhaustruct:"optional"`
	DefaultInt  int    `exhaustruct:"optional"`
	DefaultBool bool   `exhaustruct:"optional"`

	bindString func(*I, string) `exhaustruct:"optional"`
	bindInt    func(*I, int)    `exhaustruct:"optional"`
	bindBool   func(*I, bool)   `exhaustruct:"optional"`
}

// StringParam declares a free-text flag.
//
// The declaration stores def as the default value and set as the function
// that assigns the parsed string to the input. The required argument
// controls whether Cobra requires the flag.
//
// For example, an operation can declare a required name:
//
//	StringParam("name", "Set the name.", "", true,
//		func(input *nameInput, value string) { input.Name = value })
func StringParam[I Input](flag, description, def string, required bool, set func(*I, string)) Param[I] {
	return Param[I]{
		Kind: kindString, Flag: flag, Description: description, Required: required,
		DefaultStr: def, DefaultInt: 0, DefaultBool: false,
		bindString: set, bindInt: nil, bindBool: nil,
	}
}

// IntParam declares a whole-number flag.
//
// The declaration stores def as the default value and set as the function
// that assigns the parsed integer to the input. Cobra does not require
// this flag.
//
// For example, an operation can declare a count:
//
//	IntParam("count", "Set the count.", 1,
//		func(input *countInput, value int) { input.Count = value })
func IntParam[I Input](flag, description string, def int, set func(*I, int)) Param[I] {
	return Param[I]{
		Kind: kindInt, Flag: flag, Description: description, Required: false,
		DefaultStr: "", DefaultInt: def, DefaultBool: false,
		bindString: nil, bindInt: set, bindBool: nil,
	}
}

// BoolParam declares a flag that accepts true or false.
//
// The declaration stores def as the default value and set as the function
// that assigns the parsed Boolean to the input. Cobra does not require
// this flag.
//
// For example, an operation can declare an enabled flag:
//
//	BoolParam("enabled", "Enable the operation.", false,
//		func(input *enabledInput, value bool) { input.Enabled = value })
func BoolParam[I Input](flag, description string, def bool, set func(*I, bool)) Param[I] {
	return Param[I]{
		Kind: kindBool, Flag: flag, Description: description, Required: false,
		DefaultStr: "", DefaultInt: 0, DefaultBool: def,
		bindString: nil, bindInt: nil, bindBool: set,
	}
}
