package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
)

// RepairCheckpointWriter reads repair checkpoints and updates their phases.
type RepairCheckpointWriter interface {
	RepairCheckpointReader
	AdvanceRepairCheckpoint(ctx context.Context, previous node.RepairPhase, next node.RepairCheckpoint) error
}

// repairPhaseRunner executes one repair phase and records its checkpoint.
type repairPhaseRunner struct {
	checkpoints RepairCheckpointWriter
	exports     RepairExportReader
}

// runs reports whether the runner executes phase. The runner executes the
// export phase, which changes no node record.
func (runner repairPhaseRunner) runs(phase node.RepairPhase) bool {
	return phase == node.RepairPhaseExported
}

// runPhase executes phase for entry and advances the checkpoint of entry
// from its stored phase to phase.
func (runner repairPhaseRunner) runPhase(ctx context.Context, manifestDigest string, operator audit.OperatorPrincipal, entry NodeIntegrityEntry, phase node.RepairPhase) error {
	stored, err := runner.checkpoints.RepairCheckpoint(ctx, entry.RepairID)
	if err != nil {
		return repairPhaseFailure(ctx, entry, phase, err)
	}
	previous := node.RepairPhase("")
	if stored != nil {
		previous = stored.Phase
	}
	digest, err := exportRepairNode(ctx, runner.exports, entry, manifestDigest, operator.ID)
	if err != nil {
		return repairPhaseFailure(ctx, entry, phase, err)
	}
	next := node.RepairCheckpoint{
		RepairID: entry.RepairID, ManifestDigest: manifestDigest, NodeID: entry.NodeID, SourceDigest: entry.Source.PrimaryDigest,
		Phase: phase, ExportPath: entry.Delete.ExportFile, ExportDigest: digest, ActorID: operator.ID, UpdatedAt: clock.Now().UTC(),
	}
	if err := runner.checkpoints.AdvanceRepairCheckpoint(ctx, previous, next); err != nil {
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
