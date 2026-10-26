package foundationdb

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

// AllocateSequence atomically increments and returns the next sequence number.
// The transaction also writes one change event with the old and new counter
// values. It refuses to change the counter when ctx lacks a mutation operation.
func (s *NodeStore) AllocateSequence(ctx context.Context, orgID, scopeNodeID uuid.UUID, nodeType string) (seq int64, err error) {
	defer telemetry.FDBOp(ctx, "store.node.allocate_sequence")(&err)
	key := fdb.Key(sequenceKey(orgID, scopeNodeID, nodeType))
	seq, _, err = s.writeSequenceWithAudit(ctx, orgID, "node.allocate_sequence", key,
		scopeSequenceTarget(orgID, scopeNodeID, nodeType), incrementSequence)
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
// counterKey and returns the incremented value. The transaction also writes
// one change event with the old and new counter values. It refuses to change
// the counter when ctx lacks a mutation operation.
func (s *NodeStore) AllocateSequenceByKey(ctx context.Context, orgID uuid.UUID, counterKey string) (seq int64, err error) {
	defer telemetry.FDBOp(ctx, "store.node.allocate_sequence_by_key")(&err)
	seq, _, err = s.writeSequenceWithAudit(ctx, orgID, "node.allocate_sequence_by_key",
		fdb.Key(sequenceByKeyKey(orgID, counterKey)), keySequenceTarget(orgID, counterKey), incrementSequence)
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

// SeedSequenceByKey raises counterKey to value without lowering it. When the
// counter changes, the transaction also writes one change event with the old
// and new counter values. It refuses to change the counter when ctx lacks a
// mutation operation.
func (s *NodeStore) SeedSequenceByKey(ctx context.Context, orgID uuid.UUID, counterKey string, value int64) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.seed_sequence_by_key")(&err)
	_, _, err = s.writeSequenceWithAudit(ctx, orgID, "node.seed_sequence_by_key",
		fdb.Key(sequenceByKeyKey(orgID, counterKey)), keySequenceTarget(orgID, counterKey), raiseSequence(value))
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
// reports whether the counter changed. When the counter changes, the
// transaction also writes one change event with the old and new counter
// values. It refuses to change the counter when ctx lacks a mutation
// operation.
func (s *NodeStore) RaiseSequenceByKey(ctx context.Context, orgID uuid.UUID, counterKey string, value int64) (raised bool, err error) {
	defer telemetry.FDBOp(ctx, "store.node.raise_sequence_by_key")(&err)
	_, raised, err = s.writeSequenceWithAudit(ctx, orgID, "node.raise_sequence_by_key",
		fdb.Key(sequenceByKeyKey(orgID, counterKey)), keySequenceTarget(orgID, counterKey), raiseSequence(value))
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

// writeSequenceWithAudit reads the mutation operation from ctx, then applies
// change to the counter at key in one transaction with its change event. It
// waits for the counter lock of key before it creates the transaction and
// frees the lock after the transaction ends.
func (s *NodeStore) writeSequenceWithAudit(
	ctx context.Context, orgID uuid.UUID, operation string, key fdb.Key, target audit.Target, change sequenceChange,
) (int64, bool, error) {
	changes, err := beginMutationAudit(ctx, orgID)
	if err != nil {
		return 0, false, err
	}
	release, err := s.sequenceLocks.acquire(ctx, key)
	if err != nil {
		return 0, false, err
	}
	defer release()
	var stored int64
	var changed bool
	err = runNodeMutation(ctx, s.db, operation, func(tr fdb.Transaction) error {
		var writeErr error
		stored, changed, writeErr = writeAuditedSequence(ctx, tr, changes, key, target, change)
		return writeErr
	})
	return stored, changed, err
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
