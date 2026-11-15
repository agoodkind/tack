package clispec

// Arg defines one positional argument and a function that sets its input field.
type Arg[OperationInput Input] struct {
	Name        string
	Description string
	bind        func(*OperationInput, string)
}

// StringArg defines a required positional string argument.
func StringArg[OperationInput Input](name, description string, set func(*OperationInput, string)) Arg[OperationInput] {
	return Arg[OperationInput]{Name: name, Description: description, bind: set}
}

// placeholder renders the argument in a usage line, for example <backup-dir>.
func (a Arg[OperationInput]) placeholder() string {
	return "<" + a.Name + ">"
}
