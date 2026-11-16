package clispec

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

// AuditVerbAnnotation stores a command's audit verb in Cobra metadata.
const AuditVerbAnnotation = "audit_verb"

// AuditClassAnnotation is the Cobra annotation key for the audit class a
// command records. Its value is read, mutation, or external, or an empty string
// when the command does not record audit events.
const AuditClassAnnotation = "audit_class"

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

	backfills := backfillGroups{}

	for _, op := range reg.ops {
		cmd := op.cobraCommand(f)
		if g := op.renderParent(backfills); g != nil {
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

// cobraCommand renders one operation. Flags come from the parameters, the
// positional placeholders and exact count come from the arguments, and RunE
// decodes both into a fresh input before calling the work function.
func (op Operation[OperationInput]) cobraCommand(f *cli.Factory) *cobra.Command {
	var use strings.Builder
	use.WriteString(op.terminalName())

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
			AuditVerbAnnotation:  op.Audit.Verb,
			AuditClassAnnotation: string(op.Audit.Class),
		},
	}

	applies := make([]func(in *OperationInput), 0, len(op.Params))
	for _, param := range op.Params {
		applies = append(applies, registerFlag(cmd, param))
	}

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		defer applyHangupPolicy(cmd.Context(), op.auditSpec(), f.Execute())()

		in := op.New()
		for i, arg := range op.Args {
			arg.bind(&in, args[i])
		}

		for _, apply := range applies {
			apply(&in)
		}

		ctx, err := inputContext(cmd.Context(), op.auditSpec().Verb, in)
		if err != nil {
			return err
		}

		if op.auditSpec().Class == audit.OperationRead {
			return runReadOperation(ctx, f, op, in)
		}

		if !f.Execute() && op.DryRun != nil {
			err := RunAudited(
				ctx,
				dryRunDestination(f, f.Out),
				op.auditSpec(),
				f.OperatorIdentitySource(),
				f.DeployCommit(),
				false,
				f.AuditOutbox(),
				f.AuditInfrastructureProbe(),
				func(context.Context) error { return nil },
			)
			if err != nil {
				return err
			}

			return RunRecordedPreview(
				ctx,
				f,
				PreviewSpec(op.auditSpec()),
				json.RawMessage("{}"),
				func(ctx context.Context, sink ResultSink) error {
					return op.DryRun(ctx, in, sink)
				},
			)
		}

		return RunAudited(
			ctx,
			dryRunDestination(f, f.Out),
			op.auditSpec(),
			f.OperatorIdentitySource(),
			f.DeployCommit(),
			f.Execute(),
			f.AuditOutbox(),
			f.AuditInfrastructureProbe(),
			func(ctx context.Context) error {
				return op.Run(ctx, in, NewCLISink(f))
			},
		)
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
	cmd.Annotations[AuditClassAnnotation] = string(spec.Class)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		defer applyHangupPolicy(cmd.Context(), spec, f.Execute())()

		return RunAudited(
			cmd.Context(),
			dryRunDestination(f, cmd.OutOrStdout()),
			spec,
			f.OperatorIdentitySource(),
			f.DeployCommit(),
			f.Execute(),
			f.AuditOutbox(),
			f.AuditInfrastructureProbe(),
			run,
		)
	}
}
