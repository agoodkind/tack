package clispec

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

// CheckOutcome pairs the audit outcome with the command verdict that RunE
// returns after the audit gate saves the outcome.
type CheckOutcome struct {
	Details audit.OutcomeDetails
	Verdict error
}

// ExternalCheck runs a check against another system, records each read through
// steps, and returns the audit outcome and command verdict.
type ExternalCheck func(ctx context.Context, steps audit.StepRecorder) (CheckOutcome, error)

// AttachExternalCheck sets audit metadata and one RunE handler on a Cobra
// command. The handler calls RunAuditedExternal and returns the check verdict
// after the audit gate saves the outcome. The verdict does not change the
// audit outcome.
func AttachExternalCheck(cmd *cobra.Command, f *cli.Factory, spec audit.Spec, input func() (json.RawMessage, error), check ExternalCheck) {
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string)
	}
	cmd.Annotations[AuditVerbAnnotation] = spec.Verb
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		encoded, err := input()
		if err != nil {
			return loggedAuditError(cmd.Context(), "encode command input", err)
		}
		var verdict error
		run := func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
			outcome, runErr := check(ctx, steps)
			verdict = outcome.Verdict
			return outcome.Details, runErr
		}
		err = RunAuditedExternal(cmd.Context(), dryRunDestination(f, cmd.OutOrStdout()), spec, encoded,
			f.OperatorIdentitySource(), f.DeployCommit(), f.Execute(), f.AuditOutbox(), f.AuditInfrastructureProbe(), run)
		if err != nil {
			return err
		}
		return verdict
	}
}
