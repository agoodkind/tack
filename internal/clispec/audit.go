package clispec

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// RunAudited records an operator command before running it. A command that
// creates the audit infrastructure records after it establishes a missing outbox.
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
	return runAudited(
		withDryRunOutput(ctx, output),
		spec,
		source,
		deployCommit,
		execute,
		outbox,
		probe,
		run,
	)
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
		return loggedAuditError(
			ctx,
			"resolve operator",
			errors.New("operator identity source is nil"),
		)
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

	if err := outbox.WriteOutbox(ctx, event); err != nil {
		if spec.CreatesAuditInfrastructure {
			return runAfterCreatingAuditInfrastructure(
				ctx,
				event,
				runContext,
				outbox,
				probe,
				run,
				err,
			)
		}

		return loggedAuditError(ctx, "record command intent", err)
	}

	return run(runContext)
}

func validateAuditSpec(spec audit.Spec) error {
	if spec.Verb == "" {
		return errors.New("audit spec must declare a verb")
	}

	if spec.Atomic {
		// A FoundationDB command records inside its own transaction, and the
		// gate must verify it actually did. That machinery lands with the
		// first such command; until then declaring Atomic fails loudly rather
		// than falling through to the outbox path and quietly losing the
		// guarantee the flag promises.
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
