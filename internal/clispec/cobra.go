package clispec

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

// AuditVerbAnnotation stores a command's audit verb in Cobra metadata.
const AuditVerbAnnotation = "audit_verb"

// GroupAnnotation marks a Cobra help-only command group.
const GroupAnnotation = "clispec_group"

// RenderCobra turns the registry into top-level cobra commands. Operations and
// handwritten commands attach under their group, and parents are created once
// per *Group value.
func RenderCobra(reg *Registry, f *cli.Factory) []*cobra.Command {
	parents := map[*Group]*cobra.Command{}
	var tops []*cobra.Command

	var ensure func(g *Group) *cobra.Command
	ensure = func(g *Group) *cobra.Command {
		if c, ok := parents[g]; ok {
			return c
		}
		c := newGroupCommand(g)
		parents[g] = c
		if g.Parent == nil {
			tops = append(tops, c)
		} else {
			ensure(g.Parent).AddCommand(c)
		}
		return c
	}

	for _, op := range reg.ops {
		cmd := op.cobraCommand(f)
		if g := op.group(); g != nil {
			ensure(g).AddCommand(cmd)
		} else {
			tops = append(tops, cmd)
		}
	}
	for _, h := range reg.handwritten {
		cmd := h.Build(f)
		if h.Group != nil {
			ensure(h.Group).AddCommand(cmd)
		} else {
			tops = append(tops, cmd)
		}
	}
	return tops
}

// newGroupCommand builds a parent whose bare invocation prints help.
func newGroupCommand(g *Group) *cobra.Command {
	cmd := &cobra.Command{
		Use:   g.Use,
		Short: g.Short,
		Long:  g.Long,
		Annotations: map[string]string{
			GroupAnnotation: "true",
		},
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
	return cmd
}

// cobraCommand renders one operation. Flags come from the parameters, the
// positional placeholders and exact count come from the arguments, and RunE
// decodes both into a fresh input before calling the work function.
func (op Operation[I]) cobraCommand(f *cli.Factory) *cobra.Command {
	var use strings.Builder
	use.WriteString(op.Name.CLI())
	for _, arg := range op.Args {
		use.WriteString(" ")
		use.WriteString(arg.placeholder())
	}
	cmd := &cobra.Command{
		Use:     use.String(),
		Aliases: op.Aliases,
		Hidden:  op.Hidden,
		Short:   op.Short,
		Long:    op.longHelp(),
		Example: strings.Join(op.Examples, "\n"),
		Args:    cobra.ExactArgs(len(op.Args)),
		Annotations: map[string]string{
			AuditVerbAnnotation: op.Audit.Verb,
		},
	}
	applies := make([]func(in *I), 0, len(op.Params))
	for _, param := range op.Params {
		applies = append(applies, registerFlag(cmd, param))
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		in := op.New()
		for i, arg := range op.Args {
			arg.bind(&in, args[i])
		}
		for _, apply := range applies {
			apply(&in)
		}
		if !f.Execute() && op.DryRun != nil {
			err := RunAudited(cmd.Context(), f.Out, op.auditSpec(), f.OperatorIdentitySource(), f.DeployCommit(), false, f.AuditOutbox(), f.AuditInfrastructureProbe(),
				func(context.Context) error { return nil })
			if err != nil {
				return err
			}
			return op.DryRun(cmd.Context(), in, NewCLISink(f))
		}
		return RunAudited(cmd.Context(), f.Out, op.auditSpec(), f.OperatorIdentitySource(), f.DeployCommit(), f.Execute(), f.AuditOutbox(), f.AuditInfrastructureProbe(),
			func(ctx context.Context) error {
				return op.Run(ctx, in, NewCLISink(f))
			})
	}
	return cmd
}

// AttachAudit adds audit metadata and an audited RunE to a Cobra command.
func AttachAudit(
	cmd *cobra.Command,
	f *cli.Factory,
	spec audit.Spec,
	run func(context.Context) error,
) {
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string)
	}
	cmd.Annotations[AuditVerbAnnotation] = spec.Verb
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return RunAudited(cmd.Context(), cmd.OutOrStdout(), spec, f.OperatorIdentitySource(), f.DeployCommit(), f.Execute(), f.AuditOutbox(), f.AuditInfrastructureProbe(), run)
	}
}

// longHelp builds the --help body: the author's Long (or Short), then a
// documented Arguments section so each placeholder is explained in place.
func (op Operation[I]) longHelp() string {
	var b strings.Builder
	switch {
	case op.Long != "":
		b.WriteString(op.Long)
	case op.Short != "":
		b.WriteString(op.Short)
	}
	if len(op.Args) == 0 {
		return b.String()
	}
	width := 0
	for _, arg := range op.Args {
		if len(arg.placeholder()) > width {
			width = len(arg.placeholder())
		}
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("Arguments:")
	for _, arg := range op.Args {
		fmt.Fprintf(&b, "\n  %-*s  %s", width, arg.placeholder(), arg.Description)
	}
	return b.String()
}

// registerFlag registers one parameter as a flag and returns a closure that
// copies the parsed value into the input after cobra parses the command line.
func registerFlag[I Input](cmd *cobra.Command, param Param[I]) func(in *I) {
	switch param.Kind {
	case kindString:
		holder := new(string)
		cmd.Flags().StringVar(holder, param.Flag, param.DefaultStr, param.Description)
		markRequired(cmd, param)
		bind := param.bindString
		return func(in *I) { bind(in, *holder) }
	case kindInt:
		holder := new(int)
		cmd.Flags().IntVar(holder, param.Flag, param.DefaultInt, param.Description)
		bind := param.bindInt
		return func(in *I) { bind(in, *holder) }
	case kindBool:
		holder := new(bool)
		cmd.Flags().BoolVar(holder, param.Flag, param.DefaultBool, param.Description)
		bind := param.bindBool
		return func(in *I) { bind(in, *holder) }
	default:
		return func(in *I) {}
	}
}

func markRequired[I Input](cmd *cobra.Command, param Param[I]) {
	if param.Required {
		_ = cmd.MarkFlagRequired(param.Flag)
	}
}
