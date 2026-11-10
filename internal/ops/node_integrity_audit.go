package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
)

// stageRepairEvent attaches the audit operation for one repair phase to the
// returned context. The operation keeps the operator principal and operation
// ID that the command gate attaches, uses nodeIntegrityBackfillTool as its
// tool name, and records the repair ID and phase as its reason. The checked
// mutation records one change event for each record that its transaction
// changes.
func stageRepairEvent(ctx context.Context, run repairPhaseRun, phase node.RepairPhase) (context.Context, error) {
	identity := run.entry.RepairID + ":" + string(phase)
	operation, err := audit.RequireMutationContext(ctx)
	if err != nil {
		wrapped := fmt.Errorf("read the audit operation of repair %s: %w", identity, err)
		slog.ErrorContext(ctx, "repair.event_failed", slog.String("err", wrapped.Error()), slog.String("repair_id", run.entry.RepairID))
		return ctx, wrapped
	}
	operation.Context.Tool = nodeIntegrityBackfillTool
	operation.Context.Reason = nodeIntegrityTicket + " repair " + identity
	staged, err := audit.WithMutationContext(ctx, operation)
	if err != nil {
		wrapped := fmt.Errorf("attach the audit operation of repair %s: %w", identity, err)
		slog.ErrorContext(ctx, "repair.event_failed", slog.String("err", wrapped.Error()), slog.String("repair_id", run.entry.RepairID))
		return ctx, wrapped
	}
	return staged, nil
}
