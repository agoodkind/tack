package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

// PeekSequenceByKey reads counterKey without changing it. A counter that has
// never been written reads as zero.
func (s *NodeStore) PeekSequenceByKey(
	ctx context.Context,
	orgID uuid.UUID,
	counterKey string,
) (value int64, err error) {
	defer telemetry.FDBOp(ctx, "store.node.peek_sequence_by_key")(&err)
	key := fdb.Key(sequenceByKeyKey(orgID, counterKey))
	// db.ReadTransact is unavailable here: its closure must return an empty
	// interface, and the repo's staticcheck rule refuses a new one. This is
	// that retry loop spelled out, over a read transaction.
	transaction, transactionErr := s.db.CreateTransaction()
	if transactionErr != nil {
		slog.ErrorContext(ctx, "node.peek_sequence_transaction_failed",
			slog.String("counter_key", counterKey), slog.String("err", transactionErr.Error()))
		return 0, fmt.Errorf("create peek transaction for %q: %w", counterKey, transactionErr)
	}
	defer transaction.Cancel()
	for {
		current, readErr := readSequence(transaction, key)
		if readErr == nil {
			return current, nil
		}
		var fdbErr fdb.Error
		if !errors.As(readErr, &fdbErr) {
			slog.ErrorContext(ctx, "node.peek_sequence_failed",
				slog.String("counter_key", counterKey), slog.String("err", readErr.Error()))
			return 0, fmt.Errorf("fdb peek sequence for %q: %w", counterKey, readErr)
		}
		if retryErr := transaction.OnError(fdbErr).Get(); retryErr != nil {
			slog.ErrorContext(ctx, "node.peek_sequence_retry_failed",
				slog.String("counter_key", counterKey), slog.String("err", retryErr.Error()))
			return 0, fmt.Errorf("retry peek sequence for %q: %w", counterKey, retryErr)
		}
	}
}

// RaiseSequenceByKey raises counterKey to value without lowering it and
// reports whether the counter changed. A counter already at or above value is
// left alone, so a repeated seed is a read and not a write, and a caller that
// records seeds in the ledger records only the ones that happened.
func (s *NodeStore) RaiseSequenceByKey(
	ctx context.Context,
	orgID uuid.UUID,
	counterKey string,
	value int64,
) (raised bool, err error) {
	defer telemetry.FDBOp(ctx, "store.node.raise_sequence_by_key")(&err)
	key := fdb.Key(sequenceByKeyKey(orgID, counterKey))
	// db.Transact is unavailable here for the reason PeekSequenceByKey gives,
	// so this is its retry loop spelled out: read, compare, write, commit.
	transaction, transactionErr := s.db.CreateTransaction()
	if transactionErr != nil {
		slog.ErrorContext(ctx, "node.raise_sequence_transaction_failed",
			slog.String("counter_key", counterKey), slog.String("err", transactionErr.Error()))
		return false, fmt.Errorf("create raise transaction for %q: %w", counterKey, transactionErr)
	}
	defer transaction.Cancel()
	for {
		current, attemptErr := readSequence(transaction, key)
		if attemptErr == nil && current >= value {
			return false, nil
		}
		if attemptErr == nil {
			attemptErr = writeSequence(transaction, key, value)
		}
		if attemptErr == nil {
			attemptErr = transaction.Commit().Get()
		}
		if attemptErr == nil {
			return true, nil
		}
		var fdbErr fdb.Error
		if !errors.As(attemptErr, &fdbErr) {
			slog.ErrorContext(ctx, "node.raise_sequence_failed",
				slog.String("counter_key", counterKey), slog.String("err", attemptErr.Error()))
			return false, fmt.Errorf("fdb raise sequence for %q: %w", counterKey, attemptErr)
		}
		if retryErr := transaction.OnError(fdbErr).Get(); retryErr != nil {
			slog.ErrorContext(ctx, "node.raise_sequence_retry_failed",
				slog.String("counter_key", counterKey), slog.String("err", retryErr.Error()))
			return false, fmt.Errorf("retry raise sequence for %q: %w", counterKey, retryErr)
		}
	}
}
