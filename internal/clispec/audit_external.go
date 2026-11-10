package clispec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

// ExternalRun runs an operator command that changes another system. It
// records each effect through steps and returns the observed outcome.
type ExternalRun func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error)

// RunAuditedExternal records an external operator command with format 2
// events. When execute is true, it creates a new operation ID and saves the
// intent before calling run. The intent stores the target and safe input
// that spec selects from input. The resolved principal identifies the actor,
// and Extra stores any accountable operator. When execute is false,
// RunAuditedExternal prints the dry-run preamble without calling run.
//
// When outbox does not save the intent of a spec that creates the audit
// infrastructure, RunAuditedExternal checks the infrastructure with probe
// and can run the command with its events in memory. It writes those events
// to outbox after the command runs.
func RunAuditedExternal(
	ctx context.Context,
	output io.Writer,
	spec audit.Spec,
	input json.RawMessage,
	source audit.OperatorIdentitySource,
	deployCommit string,
	execute bool,
	outbox audit.OutboxWriter,
	probe audit.InfrastructureProbe,
	run ExternalRun,
) error {
	ctx = withDryRunOutput(ctx, output)
	if spec.Class != audit.OperationExternal {
		return loggedAuditError(ctx, "validate audit spec",
			fmt.Errorf("audit spec %q requires the external class", spec.Verb))
	}
	target, safeInput, err := operationTarget(ctx, spec, input)
	if err != nil {
		return err
	}
	if source == nil {
		return loggedAuditError(ctx, "resolve operator", errors.New("operator identity source is nil"))
	}
	principal, err := source.Resolve(ctx)
	if err != nil {
		return loggedAuditError(ctx, "resolve operator", err)
	}
	if !execute {
		return printDryRun(ctx, principal, spec)
	}
	operationID := uuid.Must(uuid.NewV7())
	intent, err := newCompleteOperatorEvent(ctx, spec, principal, operationID, deployCommit, target, audit.PhaseIntent, safeInput)
	if err != nil {
		return err
	}
	intent.Outcome = audit.OutcomePending
	runContext := audit.WithOperatorPrincipal(audit.WithOpID(ctx, operationID), principal)
	err = audit.RunExternal(runContext, outbox, intent, func(stepContext context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
		return run(stepContext, steps)
	})
	if errors.Is(err, audit.ErrExternalIntentNotSaved) && spec.CreatesAuditInfrastructure {
		err = runExternalCreatingAuditInfrastructure(ctx, runContext, outbox, probe, intent, run, err)
	}
	if errors.Is(err, audit.ErrExternalIntentNotSaved) {
		return loggedAuditError(ctx, "record command intent", err)
	}
	if err != nil {
		slog.ErrorContext(ctx, "cli.external_command_failed", slog.String("operation", spec.Verb), slog.String("err", err.Error()))
		return fmt.Errorf("%s: %w", spec.Verb, err)
	}
	return nil
}

// AttachExternal sets audit metadata and RunE on a handwritten Cobra command
// by calling AttachExternalPreview with a nil preview.
func AttachExternal(cmd *cobra.Command, f *cli.Factory, spec audit.Spec, input func() (json.RawMessage, error), run ExternalRun) {
	AttachExternalPreview(cmd, f, spec, input, run, nil)
}

// AttachExternalPreview sets audit metadata and RunE on a handwritten Cobra
// command. RunE encodes input and calls RunAuditedExternal. When the operator
// omits --execute and RunAuditedExternal succeeds, RunE calls preview if
// preview is not nil.
func AttachExternalPreview(
	cmd *cobra.Command,
	f *cli.Factory,
	spec audit.Spec,
	input func() (json.RawMessage, error),
	run ExternalRun,
	preview func(context.Context) error,
) {
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string)
	}
	cmd.Annotations[AuditVerbAnnotation] = spec.Verb
	cmd.Annotations[AuditClassAnnotation] = string(spec.Class)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		encoded, err := input()
		if err != nil {
			return loggedAuditError(cmd.Context(), "encode command input", err)
		}
		err = RunAuditedExternal(cmd.Context(), dryRunDestination(f, cmd.OutOrStdout()), spec, encoded,
			f.OperatorIdentitySource(), f.DeployCommit(), f.Execute(), f.AuditOutbox(), f.AuditInfrastructureProbe(), run)
		if err != nil || f.Execute() || preview == nil {
			return err
		}
		return preview(cmd.Context())
	}
}
