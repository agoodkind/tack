package foundationdb

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

// AllocateSequence atomically increments and returns the next sequence number.
func (s *NodeStore) AllocateSequence(ctx context.Context, orgID, scopeNodeID uuid.UUID, nodeType string) (seq int64, err error) {
	defer telemetry.FDBOp(ctx, "store.node.allocate_sequence")(&err)
	err = runNodeMutation(ctx, s.db, "node.allocate_sequence", func(tr fdb.Transaction) error {
		var next int64
		next, err = bumpSequence(ctx, tr, fdb.Key(sequenceKey(orgID, scopeNodeID, nodeType)), 1)
		seq = next
		return err
	})
	if err != nil {
		if searchFailureWasLogged(err) {
			return 0, err
		}
		wrapped := fmt.Errorf("fdb allocate sequence: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "node.allocate_sequence_failed", slog.String("err", wrapped.Error()), slog.String("org_id", orgID.String()))
		return 0, wrapped
	}
	return seq, nil
}

// AllocateSequenceByKey atomically increments the sequence stored under
// counterKey and returns the incremented value.
func (s *NodeStore) AllocateSequenceByKey(ctx context.Context, orgID uuid.UUID, counterKey string) (seq int64, err error) {
	defer telemetry.FDBOp(ctx, "store.node.allocate_sequence_by_key")(&err)
	err = runNodeMutation(ctx, s.db, "node.allocate_sequence_by_key", func(tr fdb.Transaction) error {
		var next int64
		var bumpErr error
		next, bumpErr = bumpSequence(ctx, tr, fdb.Key(sequenceByKeyKey(orgID, counterKey)), 1)
		seq = next
		return bumpErr
	})
	if err != nil {
		if searchFailureWasLogged(err) {
			return 0, err
		}
		wrapped := fmt.Errorf("fdb allocate sequence for %q: %w", counterKey, err)
		telemetry.L(ctx).ErrorContext(ctx, "node.allocate_sequence_by_key_failed", slog.String("counter_key", counterKey), slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	return seq, nil
}

// SeedSequenceByKey raises counterKey to value without lowering it.
func (s *NodeStore) SeedSequenceByKey(ctx context.Context, orgID uuid.UUID, counterKey string, value int64) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.seed_sequence_by_key")(&err)
	err = runNodeMutation(ctx, s.db, "node.seed_sequence_by_key", func(tr fdb.Transaction) error {
		key := fdb.Key(sequenceByKeyKey(orgID, counterKey))
		current, readErr := readSequence(ctx, tr, key)
		if readErr != nil || current >= value {
			return readErr
		}
		return writeSequence(tr, key, value)
	})
	if err != nil {
		if searchFailureWasLogged(err) {
			return err
		}
		wrapped := fmt.Errorf("fdb seed sequence for %q: %w", counterKey, err)
		telemetry.L(ctx).ErrorContext(ctx, "node.seed_sequence_by_key_failed", slog.String("counter_key", counterKey), slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}

// PeekSequenceByKey reads counterKey without changing it.
func (s *NodeStore) PeekSequenceByKey(ctx context.Context, orgID uuid.UUID, counterKey string) (value int64, err error) {
	defer telemetry.FDBOp(ctx, "store.node.peek_sequence_by_key")(&err)
	var raw []byte
	err = runNodeReadTransaction(ctx, s.db, "node.peek_sequence_by_key", func(tr fdb.Transaction) error {
		var readErr error
		raw, readErr = tr.Get(fdb.Key(sequenceByKeyKey(orgID, counterKey))).Get()
		if readErr != nil {
			wrapped := fmt.Errorf("read sequence for %q: %w", counterKey, readErr)
			telemetry.L(ctx).ErrorContext(ctx, "node.sequence.read_failed", slog.String("err", wrapped.Error()), slog.String("counter_key", counterKey))
			return loggedSearchError{err: wrapped}
		}
		return nil
	})
	if err != nil {
		if searchFailureWasLogged(err) {
			return 0, err
		}
		wrapped := fmt.Errorf("read sequence for %q: %w", counterKey, err)
		telemetry.L(ctx).ErrorContext(ctx, "node.peek_sequence_failed", slog.String("err", wrapped.Error()), slog.String("counter_key", counterKey))
		return 0, wrapped
	}
	decoded, decodeErr := readEncodedSequence(raw)
	if decodeErr != nil {
		wrapped := fmt.Errorf("decode sequence for %q: %w", counterKey, decodeErr)
		telemetry.L(ctx).ErrorContext(ctx, "node.peek_sequence_decode_failed", slog.String("err", wrapped.Error()), slog.String("counter_key", counterKey))
		return 0, wrapped
	}
	return decoded, nil
}

// RaiseSequenceByKey raises counterKey to value without lowering it and
// reports whether the counter changed.
func (s *NodeStore) RaiseSequenceByKey(ctx context.Context, orgID uuid.UUID, counterKey string, value int64) (raised bool, err error) {
	defer telemetry.FDBOp(ctx, "store.node.raise_sequence_by_key")(&err)
	err = runNodeMutation(ctx, s.db, "node.raise_sequence_by_key", func(tr fdb.Transaction) error {
		key := fdb.Key(sequenceByKeyKey(orgID, counterKey))
		current, readErr := readSequence(ctx, tr, key)
		if readErr != nil || current >= value {
			return readErr
		}
		if writeErr := writeSequence(tr, key, value); writeErr != nil {
			return writeErr
		}
		raised = true
		return nil
	})
	if err != nil {
		if searchFailureWasLogged(err) {
			return false, err
		}
		wrapped := fmt.Errorf("raise sequence for %q: %w", counterKey, err)
		telemetry.L(ctx).ErrorContext(ctx, "node.raise_sequence_failed", slog.String("err", wrapped.Error()), slog.String("counter_key", counterKey))
		return false, wrapped
	}
	return raised, nil
}

func readSequence(ctx context.Context, tr fdb.Transaction, key fdb.Key) (int64, error) {
	raw, err := tr.Get(key).Get()
	if err != nil {
		wrapped := fmt.Errorf("read sequence key: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "node.sequence.read_failed", slog.String("err", wrapped.Error()))
		return 0, loggedSearchError{err: wrapped}
	}
	return readEncodedSequence(raw)
}

func readEncodedSequence(raw []byte) (int64, error) {
	if len(raw) < 8 {
		return 0, nil
	}
	value := binary.LittleEndian.Uint64(raw)
	if value > math.MaxInt64 {
		return 0, fmt.Errorf("sequence value %d exceeds int64", value)
	}
	return int64(value), nil
}

func writeSequence(tr fdb.Transaction, key fdb.Key, value int64) error {
	if value < 0 {
		return fmt.Errorf("sequence value %d is negative", value)
	}
	buffer := make([]byte, 8)
	binary.LittleEndian.PutUint64(buffer, uint64(value))
	tr.Set(key, buffer)
	return nil
}

func bumpSequence(ctx context.Context, tr fdb.Transaction, key fdb.Key, delta int64) (int64, error) {
	current, err := readSequence(ctx, tr, key)
	if err != nil {
		return 0, err
	}
	if delta > 0 && current > math.MaxInt64-delta {
		return 0, fmt.Errorf("sequence value %d overflows after increment %d", current, delta)
	}
	if delta < 0 && current < math.MinInt64-delta {
		return 0, fmt.Errorf("sequence value %d underflows after increment %d", current, delta)
	}
	next := current + delta
	if err := writeSequence(tr, key, next); err != nil {
		return 0, err
	}
	return next, nil
}
