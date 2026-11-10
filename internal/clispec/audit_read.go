package clispec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

// OperatorRead reads state for an operator command and returns the exact
// bytes the command prints.
type OperatorRead func(ctx context.Context) (audit.OperatorReadOutput, error)

// RunAuditedRead gives each read a new operation ID. Ordinary reads require
// admission and a saved final event before returning the payload. Operations
// that set CreatesAuditInfrastructure may use the first-time setup fallback
// after admission fails and the infrastructure probe confirms it is needed.
func RunAuditedRead(
	ctx context.Context,
	f *cli.Factory,
	spec audit.Spec,
	input json.RawMessage,
	read OperatorRead,
) ([]byte, error) {
	if spec.Class != audit.OperationRead {
		return nil, loggedAuditError(
			ctx,
			"validate audit spec",
			fmt.Errorf("audit spec %q requires the read class", spec.Verb),
		)
	}

	target, safeInput, err := operationTarget(ctx, spec, input)
	if err != nil {
		return nil, err
	}

	source := f.OperatorIdentitySource()
	if source == nil {
		return nil, loggedAuditError(
			ctx,
			"resolve operator",
			errors.New("operator identity source is nil"),
		)
	}

	principal, err := source.Resolve(ctx)
	if err != nil {
		return nil, loggedAuditError(ctx, "resolve operator", err)
	}

	operationID := uuid.Must(uuid.NewV7())
	template, err := newCompleteOperatorEvent(
		ctx,
		spec,
		principal,
		operationID,
		f.DeployCommit(),
		target,
		audit.PhaseRead,
		safeInput,
	)
	if err != nil {
		return nil, err
	}

	runContext := audit.WithOperatorPrincipal(audit.WithOpID(ctx, operationID), principal)
	payload, err := audit.RecordOperatorRead(runContext, f.AuditReads(), template, read)
	if err == nil {
		return payload, nil
	}

	if !errors.Is(err, audit.ErrReadNotAdmitted) || !spec.CreatesAuditInfrastructure {
		return nil, loggedAuditError(ctx, "record read access", err)
	}

	return readBeforeReadLifecycle(runContext, f, spec, principal, operationID, read, err)
}

// readBeforeReadLifecycle checks the audit infrastructure before running a
// read that the read lifecycle store did not admit.
func readBeforeReadLifecycle(
	ctx context.Context,
	f *cli.Factory,
	spec audit.Spec,
	principal audit.OperatorPrincipal,
	operationID uuid.UUID,
	read OperatorRead,
	admissionErr error,
) ([]byte, error) {
	probe := f.AuditInfrastructureProbe()
	if probe == nil {
		return nil, loggedAuditError(ctx, "record read access", admissionErr)
	}

	infrastructure, probeErr := probe(ctx)
	if probeErr != nil {
		return nil, loggedAuditError(ctx, "record read access", errors.Join(admissionErr, probeErr))
	}

	switch {
	case !infrastructure.OutboxTableExists || !infrastructure.OperatorLoginExists:
		slog.WarnContext(
			ctx,
			"cli.audit_read_unrecorded",
			slog.String("operation", spec.Verb),
			slog.Bool("outbox_table_exists", infrastructure.OutboxTableExists),
			slog.Bool("operator_login_exists", infrastructure.OperatorLoginExists),
		)
	case !infrastructure.OperatorReadsExist:
		if err := recordFormatZeroRead(ctx, f, spec, principal, operationID); err != nil {
			return nil, err
		}
	default:
		return nil, loggedAuditError(ctx, "record read access", admissionErr)
	}

	output, err := read(ctx)
	if err != nil {
		return nil, err
	}

	return output.Payload, nil
}

// recordFormatZeroRead saves a format 0 read event before the command reads state.
func recordFormatZeroRead(
	ctx context.Context,
	f *cli.Factory,
	spec audit.Spec,
	principal audit.OperatorPrincipal,
	operationID uuid.UUID,
) error {
	outbox := f.AuditOutbox()
	if outbox == nil {
		return loggedAuditError(ctx, "record read access", errors.New("audit outbox is nil"))
	}

	event, err := newOperatorEvent(
		ctx,
		audit.Spec{Verb: spec.Verb, Reads: true},
		principal,
		operationID,
		f.DeployCommit(),
	)
	if err != nil {
		return err
	}

	if err := outbox.WriteOutbox(ctx, event); err != nil {
		return loggedAuditError(ctx, "record read access", err)
	}

	return nil
}
