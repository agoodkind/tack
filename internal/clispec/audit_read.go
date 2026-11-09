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

// RunAuditedRead assigns a new operation ID to an operator read.
// The normal read lifecycle admits the read and saves its final event before
// returning the payload.
//
// The infrastructure fallback can return data without durable admission and
// final evidence. This behavior does not satisfy the binding requirement to
// admit reads durably and finalize their evidence before returning data.
//
//   - The fallback requires failed admission and CreatesAuditInfrastructure.
//   - A missing outbox table or operator login permits an unrecorded read.
//   - With an existing table and login, a false OperatorReadsExist result
//     selects a format 0 access event instead of the complete read lifecycle.
//   - A missing or failed probe prevents the fallback.
func RunAuditedRead(
	ctx context.Context,
	f *cli.Factory,
	spec audit.Spec,
	input json.RawMessage,
	read OperatorRead,
) ([]byte, error) {
	if spec.Class != audit.OperationRead {
		return nil, loggedAuditError(ctx, "validate audit spec", fmt.Errorf("audit spec %q requires the read class", spec.Verb))
	}
	target, safeInput, err := operationTarget(ctx, spec, input)
	if err != nil {
		return nil, err
	}
	source := f.OperatorIdentitySource()
	if source == nil {
		return nil, loggedAuditError(ctx, "resolve operator", errors.New("operator identity source is nil"))
	}
	principal, err := source.Resolve(ctx)
	if err != nil {
		return nil, loggedAuditError(ctx, "resolve operator", err)
	}
	operationID := uuid.Must(uuid.NewV7())
	template, err := newCompleteOperatorEvent(ctx, spec, principal, operationID, f.DeployCommit(), target, audit.PhaseRead, safeInput)
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

// readBeforeReadLifecycle probes audit infrastructure after admission fails.
// It can run an unrecorded read when the outbox table or login is missing.
// With both present, a false OperatorReadsExist result selects a format 0
// event before the read.
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
		slog.WarnContext(ctx, "cli.audit_read_unrecorded",
			slog.String("operation", spec.Verb),
			slog.Bool("outbox_table_exists", infrastructure.OutboxTableExists),
			slog.Bool("operator_login_exists", infrastructure.OperatorLoginExists))
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

// recordFormatZeroRead saves a format 0 read event before the command
// reads state.
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
	event, err := newOperatorEvent(ctx, audit.Spec{Verb: spec.Verb, Reads: true}, principal, operationID, f.DeployCommit())
	if err != nil {
		return err
	}
	if err := outbox.WriteOutbox(ctx, event); err != nil {
		return loggedAuditError(ctx, "record read access", err)
	}
	return nil
}
