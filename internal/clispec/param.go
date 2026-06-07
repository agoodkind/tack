package clispec

import (
	"fmt"
	"strings"
)

// paramKind names the value kinds a flag can carry.
type paramKind uint8

const (
	kindString paramKind = iota
	kindInt
	kindBool
	kindEnum
)

// Param declares one flag and the closure that copies the parsed value into
// the input struct after cobra parses the command line. The bind closures are
// typed per kind, so no reflection or any-typed plumbing is needed.
type Param[I Input] struct {
	Kind        paramKind
	Flag        string
	Description string
	Required    bool
	Values      []string

	DefaultStr  string
	DefaultInt  int
	DefaultBool bool

	bindString func(*I, string)
	bindInt    func(*I, int)
	bindBool   func(*I, bool)
}

// StringParam declares a free-text flag.
func StringParam[I Input](flag, description, def string, required bool, set func(*I, string)) Param[I] {
	return Param[I]{
		Kind:       kindString,
		Flag:       flag,
		Description: description,
		Required:   required,
		DefaultStr: def,
		bindString: set,
	}
}

// IntParam declares a whole-number flag.
func IntParam[I Input](flag, description string, def int, set func(*I, int)) Param[I] {
	return Param[I]{
		Kind:       kindInt,
		Flag:       flag,
		Description: description,
		DefaultInt: def,
		bindInt:    set,
	}
}

// BoolParam declares an on/off flag.
func BoolParam[I Input](flag, description string, def bool, set func(*I, bool)) Param[I] {
	return Param[I]{
		Kind:        kindBool,
		Flag:        flag,
		Description: description,
		DefaultBool: def,
		bindBool:    set,
	}
}

// EnumParam declares a flag constrained to a fixed set of words.
func EnumParam[I Input](flag, description, def string, values []string, set func(*I, string)) Param[I] {
	return Param[I]{
		Kind:        kindEnum,
		Flag:        flag,
		Description: description,
		Values:      values,
		DefaultStr:  def,
		bindString:  set,
	}
}

// enumValue is a pflag.Value that rejects a word outside its allowed list, so
// an enum flag validates at parse time and lists its options in help.
type enumValue struct {
	allowed []string
	value   *string
}

func (e *enumValue) String() string {
	if e.value == nil {
		return ""
	}
	return *e.value
}

func (e *enumValue) Set(raw string) error {
	candidate := strings.TrimSpace(raw)
	for _, allowed := range e.allowed {
		if candidate == allowed {
			*e.value = candidate
			return nil
		}
	}
	return fmt.Errorf("unsupported value %q (allowed: %s)", raw, strings.Join(e.allowed, ", "))
}

func (e *enumValue) Type() string {
	if len(e.allowed) > 0 {
		return strings.Join(e.allowed, "|")
	}
	return "string"
}
