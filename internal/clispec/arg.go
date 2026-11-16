package clispec

// Arg binds a required positional string to a command input.
type Arg[OperationInput Input] struct {
	Name        string
	Description string
	bind        func(*OperationInput, string)
}

// StringArg defines a required positional string argument.
func StringArg[OperationInput Input](
	name string,
	description string,
	set func(*OperationInput, string),
) Arg[OperationInput] {
	return Arg[OperationInput]{Name: name, Description: description, bind: set}
}

// placeholder renders the argument in a usage line, for example <backup-dir>.
func (a Arg[OperationInput]) placeholder() string {
	return "<" + a.Name + ">"
}
