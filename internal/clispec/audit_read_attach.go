package clispec

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

// AttachRead sets audit metadata and RunE on a handwritten Cobra command that
// reads state. When the operator passes --execute, RunE encodes input, calls
// RunAuditedRead with read, and writes the returned payload as text. Without
// --execute, RunE validates the target and operator and prints the dry-run
// preamble without calling read.
func AttachRead(cmd *cobra.Command, f *cli.Factory, spec audit.Spec, input func() (json.RawMessage, error), read OperatorRead) {
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string)
	}
	cmd.Annotations[AuditVerbAnnotation] = spec.Verb
	cmd.Annotations[AuditClassAnnotation] = string(spec.Class)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		encoded, err := input()
		if err != nil {
			return loggedAuditError(ctx, "encode command input", err)
		}
		if !f.Execute() {
			return printReadDryRun(withDryRunOutput(ctx, dryRunDestination(f, cmd.OutOrStdout())), f, spec, encoded)
		}
		payload, err := RunAuditedRead(ctx, f, spec, encoded, read)
		if err != nil {
			return err
		}
		return NewCLISink(f).WriteText(ctx, string(payload))
	}
}

// printReadDryRun selects the target and resolves the operator, then prints
// the dry-run preamble.
func printReadDryRun(ctx context.Context, f *cli.Factory, spec audit.Spec, input json.RawMessage) error {
	if _, _, err := operationTarget(ctx, spec, input); err != nil {
		return err
	}
	source := f.OperatorIdentitySource()
	if source == nil {
		return loggedAuditError(ctx, "resolve operator", errors.New("operator identity source is nil"))
	}
	principal, err := source.Resolve(ctx)
	if err != nil {
		return loggedAuditError(ctx, "resolve operator", err)
	}
	return printDryRun(ctx, principal, spec)
}
