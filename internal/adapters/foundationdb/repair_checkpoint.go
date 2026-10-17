package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

func repairCheckpointKey(repairID string) []byte {
	return withPrefix(tuple.Tuple{keyRepairCheckpoint, repairID}.Pack())
}

// RepairStore reads repair evidence and manages checkpoints for reviewed repairs.
type RepairStore struct {
	db fdb.Database
}

// Repairs returns a repair store that uses the shared database connection.
func (s *Stores) Repairs() *RepairStore {
	return &RepairStore{db: s.db}
}

// RepairCheckpoint reads the checkpoint for repairID.
// It returns nil if the checkpoint does not exist.
func (s *RepairStore) RepairCheckpoint(ctx context.Context, repairID string) (checkpoint *node.RepairCheckpoint, err error) {
	defer telemetry.FDBOp(ctx, "store.repair_checkpoint.get")(&err)
	err = runNodeReadTransaction(ctx, s.db, "read repair checkpoint "+repairID, func(tr fdb.Transaction) error {
		var readErr error
		checkpoint, readErr = readRepairCheckpoint(ctx, tr, repairID)
		return readErr
	})
	if err != nil {
		return nil, err
	}
	return checkpoint, nil
}

// AdvanceRepairCheckpoint writes next if the stored phase equals previous,
// the node ID and source digest match next, and next.Phase ranks higher.
// An empty previous requires that the checkpoint does not exist.
// If any check fails, it returns an error that wraps [domain.ErrConflict]
// and does not write the checkpoint.
func (s *RepairStore) AdvanceRepairCheckpoint(ctx context.Context, previous node.RepairPhase, next node.RepairCheckpoint) (err error) {
	defer telemetry.FDBOp(ctx, "store.repair_checkpoint.advance")(&err)
	return runNodeMutation(ctx, s.db, "advance repair checkpoint "+next.RepairID, func(tr fdb.Transaction) error {
		return advanceRepairCheckpoint(ctx, tr, previous, next)
	})
}

func advanceRepairCheckpoint(ctx context.Context, tr fdb.Transaction, previous node.RepairPhase, next node.RepairCheckpoint) error {
	stored, err := readRepairCheckpoint(ctx, tr, next.RepairID)
	if err != nil {
		return err
	}
	storedPhase := node.RepairPhase("")
	if stored != nil {
		storedPhase = stored.Phase
	}
	if storedPhase != previous {
		return repairCheckpointConflict(ctx, next, fmt.Sprintf("stored phase %q, expected %q", storedPhase, previous))
	}
	if stored != nil && (stored.SourceDigest != next.SourceDigest || stored.NodeID != next.NodeID) {
		return repairCheckpointConflict(ctx, next, "stored node or source digest differs")
	}
	if !next.Phase.Follows(storedPhase) {
		return repairCheckpointConflict(ctx, next, fmt.Sprintf("phase %q cannot follow %q", next.Phase, storedPhase))
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		wrapped := fmt.Errorf("encode repair checkpoint %s: %w", next.RepairID, err)
		slog.ErrorContext(ctx, "repair.checkpoint.encode_failed", slog.String("err", wrapped.Error()), slog.String("repair_id", next.RepairID))
		return loggedSearchError{err: wrapped}
	}
	tr.Set(fdb.Key(repairCheckpointKey(next.RepairID)), encoded)
	return nil
}

// It returns nil if the record does not exist.
func readRepairCheckpoint(ctx context.Context, tr fdb.Transaction, repairID string) (*node.RepairCheckpoint, error) {
	encoded, err := tr.Get(fdb.Key(repairCheckpointKey(repairID))).Get()
	if err != nil {
		return nil, searchReadFailure(ctx, "read repair checkpoint "+repairID, err)
	}
	if len(encoded) == 0 {
		return nil, nil
	}
	var checkpoint node.RepairCheckpoint
	if err := json.Unmarshal(encoded, &checkpoint); err != nil {
		wrapped := fmt.Errorf("decode repair checkpoint %s: %w", repairID, err)
		slog.ErrorContext(ctx, "repair.checkpoint.decode_failed", slog.String("err", wrapped.Error()), slog.String("repair_id", repairID))
		return nil, loggedSearchError{err: wrapped}
	}
	return &checkpoint, nil
}

func repairCheckpointConflict(ctx context.Context, next node.RepairCheckpoint, reason string) error {
	wrapped := fmt.Errorf("advance repair checkpoint %s to %s: %s: %w", next.RepairID, next.Phase, reason, domain.ErrConflict)
	slog.ErrorContext(ctx, "repair.checkpoint.conflict", slog.String("err", wrapped.Error()), slog.String("repair_id", next.RepairID))
	return loggedSearchError{err: wrapped}
}
