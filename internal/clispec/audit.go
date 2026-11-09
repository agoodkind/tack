package clispec

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// RunAudited records an operator command and runs its callback.
//
// The gate resolves the operator before either execution or a dry run.
//
//   - A dry run prints the operator and operation without calling run.
//   - A read saves an access event before calling run.
//   - A mutation normally saves intent before run and outcome afterward.
//   - A mutation that creates audit infrastructure can run after an intent
//     write fails. A successful probe must find a missing table or login,
//     or the intent error must report an authentication failure. The gate
//     writes the deferred intent and outcome after run.
func RunAudited(
	ctx context.Context,
	output io.Writer,
	spec audit.Spec,
	source audit.OperatorIdentitySource,
	deployCommit string,
	execute bool,
	outbox audit.OutboxWriter,
	probe audit.InfrastructureProbe,
	run func(context.Context) error,
) error {
	return runAudited(withDryRunOutput(ctx, output), spec, source, deployCommit, execute, outbox, probe, run)
}

func runAudited(
	ctx context.Context,
	spec audit.Spec,
	source audit.OperatorIdentitySource,
	deployCommit string,
	execute bool,
	outbox audit.OutboxWriter,
	probe audit.InfrastructureProbe,
	run func(context.Context) error,
) error {
	if err := validateAuditSpec(spec); err != nil {
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
	opID := uuid.Must(uuid.NewV7())
	event, err := newOperatorEvent(ctx, spec, principal, opID, deployCommit)
	if err != nil {
		return err
	}

	runContext := audit.WithOperatorPrincipal(audit.WithOpID(ctx, opID), principal)
	if outbox == nil {
		return loggedAuditError(ctx, "record audit event", errors.New("audit outbox is nil"))
	}

	if spec.Reads {
		if err := outbox.WriteOutbox(ctx, event); err != nil {
			return loggedAuditError(ctx, "record read access", err)
		}
		return run(runContext)
	}

	intent := event
	intent.Outcome = audit.OutcomePending
	if err := outbox.WriteOutbox(ctx, intent); err != nil {
		if spec.CreatesAuditInfrastructure {
			return runAfterCreatingAuditInfrastructure(
				ctx, intent, event, runContext, outbox, probe, run, err,
			)
		}
		return loggedAuditError(ctx, "record command intent", err)
	}

	runErr := run(runContext)

	// The outcome needs a separate event ID. The outbox keys rows by event ID,
	// and the consumer discards duplicates. Reusing the intent's event ID would
	// discard the outcome. Both rows store the same operation ID in Extra.
	outcome := event
	outcome.EventID = uuid.Must(uuid.NewV7())
	outcome.OccurredAt = clock.Now().UTC()
	if runErr != nil {
		outcome.Outcome = audit.OutcomeError
		outcome.Error = &audit.EventError{Code: "command_failed", Message: runErr.Error()}
	}
	if err := outbox.WriteOutbox(ctx, outcome); err != nil {
		return loggedAuditError(ctx, "record command outcome", err)
	}
	return runErr
}

func validateAuditSpec(spec audit.Spec) error {
	if spec.Verb == "" {
		return errors.New("audit spec must declare a verb")
	}
	if spec.Atomic {
		// An atomic FoundationDB command must record inside its transaction,
		// and the gate must verify that recording. Add that verification with
		// the first atomic command. Until then, reject Atomic instead of using
		// the ordinary outbox path, which cannot enforce this guarantee.
		return fmt.Errorf("audit spec %q declares Atomic, which is not supported yet", spec.Verb)
	}
	if spec.Mutates == spec.Reads {
		return fmt.Errorf("audit spec %q must mark exactly one command class", spec.Verb)
	}
	if spec.CreatesAuditInfrastructure && spec.Reads {
		return fmt.Errorf("audit spec %q cannot create audit infrastructure for a read", spec.Verb)
	}
	return nil
}
