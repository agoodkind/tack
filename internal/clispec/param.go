package clispec

// paramKind names the value kinds a flag can carry.
type paramKind uint8

const (
	kindString paramKind = iota
	kindInt
	kindBool
)

// Param declares one flag and the closure that copies the parsed value into
// the input struct after cobra parses the command line. The bind closures are
// typed per kind, so no reflection or any-typed plumbing is needed.
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
func StringParam[I Input](flag, description, def string, required bool, set func(*I, string)) Param[I] {
	return Param[I]{
		Kind: kindString, Flag: flag, Description: description, Required: required,
		DefaultStr: def, DefaultInt: 0, DefaultBool: false,
		bindString: set, bindInt: nil, bindBool: nil,
	}
}

// IntParam declares a whole-number flag.
func IntParam[I Input](flag, description string, def int, set func(*I, int)) Param[I] {
	return Param[I]{
		Kind: kindInt, Flag: flag, Description: description, Required: false,
		DefaultStr: "", DefaultInt: def, DefaultBool: false,
		bindString: nil, bindInt: set, bindBool: nil,
	}
}

// BoolParam declares an on/off flag.
func BoolParam[I Input](flag, description string, def bool, set func(*I, bool)) Param[I] {
	return Param[I]{
		Kind: kindBool, Flag: flag, Description: description, Required: false,
		DefaultStr: "", DefaultInt: 0, DefaultBool: def,
		bindString: nil, bindInt: nil, bindBool: set,
	}
}
