package clispec

// paramKind distinguishes the value types that flags accept.
type paramKind uint8

const (
	kindString paramKind = iota
	kindInt
	kindBool
)

// Param defines one flag and functions that set its parsed input field.
// Each binding accepts a concrete value type without reflection.
type Param[OperationInput Input] struct {
	Kind        paramKind
	Flag        string
	Description string
	Required    bool `exhaustruct:"optional"`

	DefaultStr  string `exhaustruct:"optional"`
	DefaultInt  int    `exhaustruct:"optional"`
	DefaultBool bool   `exhaustruct:"optional"`

	bindString func(*OperationInput, string) `exhaustruct:"optional"`
	bindInt    func(*OperationInput, int)    `exhaustruct:"optional"`
	bindBool   func(*OperationInput, bool)   `exhaustruct:"optional"`
}

// StringParam defines a free-text flag.
func StringParam[OperationInput Input](
	flag string,
	description string,
	def string,
	required bool,
	set func(*OperationInput, string),
) Param[OperationInput] {
	return Param[OperationInput]{
		Kind:        kindString,
		Flag:        flag,
		Description: description,
		Required:    required,
		DefaultStr:  def,
		DefaultInt:  0,
		DefaultBool: false,
		bindString:  set,
		bindInt:     nil,
		bindBool:    nil,
	}
}

// IntParam defines a whole-number flag.
func IntParam[OperationInput Input](
	flag string,
	description string,
	def int,
	set func(*OperationInput, int),
) Param[OperationInput] {
	return Param[OperationInput]{
		Kind:        kindInt,
		Flag:        flag,
		Description: description,
		Required:    false,
		DefaultStr:  "",
		DefaultInt:  def,
		DefaultBool: false,
		bindString:  nil,
		bindInt:     set,
		bindBool:    nil,
	}
}

// BoolParam defines a boolean flag.
func BoolParam[OperationInput Input](
	flag string,
	description string,
	def bool,
	set func(*OperationInput, bool),
) Param[OperationInput] {
	return Param[OperationInput]{
		Kind:        kindBool,
		Flag:        flag,
		Description: description,
		Required:    false,
		DefaultStr:  "",
		DefaultInt:  0,
		DefaultBool: def,
		bindString:  nil,
		bindInt:     nil,
		bindBool:    set,
	}
}
