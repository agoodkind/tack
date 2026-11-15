package clispec

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// longHelp appends argument explanations to the operation's Long or Short help.
func (op Operation[OperationInput]) longHelp() string {
	var help strings.Builder
	switch {
	case op.Long != "":
		help.WriteString(op.Long)
	case op.Short != "":
		help.WriteString(op.Short)
	}
	if len(op.Args) == 0 {
		return help.String()
	}
	width := 0
	for _, argument := range op.Args {
		if len(argument.placeholder()) > width {
			width = len(argument.placeholder())
		}
	}
	if help.Len() > 0 {
		help.WriteString("\n\n")
	}
	help.WriteString("Arguments:")
	for _, argument := range op.Args {
		fmt.Fprintf(&help, "\n  %-*s  %s", width, argument.placeholder(), argument.Description)
	}
	return help.String()
}

// registerFlag registers a parameter and returns a function that sets its
// input field after Cobra parses the command line.
func registerFlag[OperationInput Input](command *cobra.Command, parameter Param[OperationInput]) func(in *OperationInput) {
	switch parameter.Kind {
	case kindString:
		value := new(string)
		command.Flags().StringVar(value, parameter.Flag, parameter.DefaultStr, parameter.Description)
		markRequired(command, parameter)
		bind := parameter.bindString
		return func(in *OperationInput) { bind(in, *value) }
	case kindInt:
		value := new(int)
		command.Flags().IntVar(value, parameter.Flag, parameter.DefaultInt, parameter.Description)
		bind := parameter.bindInt
		return func(in *OperationInput) { bind(in, *value) }
	case kindBool:
		value := new(bool)
		command.Flags().BoolVar(value, parameter.Flag, parameter.DefaultBool, parameter.Description)
		bind := parameter.bindBool
		return func(in *OperationInput) { bind(in, *value) }
	default:
		return func(in *OperationInput) {}
	}
}

func markRequired[OperationInput Input](command *cobra.Command, parameter Param[OperationInput]) {
	if parameter.Required {
		_ = command.MarkFlagRequired(parameter.Flag)
	}
}
