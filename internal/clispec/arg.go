package clispec

// Arg declares one positional argument and the closure that copies its value
// into the input struct.
type Arg[I Input] struct {
	Name        string
	Description string
	bind        func(*I, string)
}

// StringArg declares a required positional string argument.
func StringArg[I Input](name, description string, set func(*I, string)) Arg[I] {
	return Arg[I]{Name: name, Description: description, bind: set}
}

// placeholder renders the argument in a usage line, for example <backup-dir>.
func (a Arg[I]) placeholder() string {
	return "<" + a.Name + ">"
}
