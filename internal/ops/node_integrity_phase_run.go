package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// RepairCheckpointWriter reads repair checkpoints and updates their phases.
type RepairCheckpointWriter interface {
	RepairCheckpointReader
	AdvanceRepairCheckpoint(ctx context.Context, previous node.RepairPhase, next node.RepairCheckpoint) error
}

type repairPhaseRunner struct {
	checkpoints RepairCheckpointWriter
	exports     RepairExportReader
	mutator     node.Mutator
}

func (runner repairPhaseRunner) runPhase(ctx context.Context, manifestDigest string, operator audit.OperatorPrincipal, entry NodeIntegrityEntry, phase node.RepairPhase) error {
	stored, err := runner.checkpoints.RepairCheckpoint(ctx, entry.RepairID)
	if err != nil {
		return repairPhaseFailure(ctx, entry, phase, err)
	}
	run := repairPhaseRun{manifestDigest: manifestDigest, operator: operator, entry: entry, stored: stored}
	switch {
	case phase == node.RepairPhaseExported:
		err = runner.exportPhase(ctx, run)
	case phase == node.RepairPhaseCommentCreated:
		err = runner.createReportPhase(ctx, run)
	case phase == node.RepairPhaseCommentVerified:
		err = runner.verifyReportPhase(ctx, run)
	case phase == node.RepairPhaseCompleted && entry.Delete != nil:
		err = runner.deletePhase(ctx, run)
	case phase == node.RepairPhaseCompleted:
		err = runner.resultPhase(ctx, run)
	default:
		err = fmt.Errorf("phase %q does not have a runner: %w", phase, domain.ErrInvalidArgument)
	}
	if err != nil {
		return repairPhaseFailure(ctx, entry, phase, err)
	}
	return nil
}

func repairPhaseFailure(ctx context.Context, entry NodeIntegrityEntry, phase node.RepairPhase, err error) error {
	wrapped := fmt.Errorf("repair %s phase %s for node %s: %w", entry.RepairID, phase, entry.NodeID, err)
	slog.ErrorContext(ctx, "repair.phase_failed", slog.String("err", wrapped.Error()),
		slog.String("repair_id", entry.RepairID), slog.String("phase", string(phase)))
	return wrapped
}
