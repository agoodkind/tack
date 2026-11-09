package foundationdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

// Append writes one audit event under a versionstamped outbox key in its own
// transaction and returns after the transaction commits.
//
// The versionstamp orders entries by commit for relay delivery. A caller
// that needs its data change and event to commit together appends within
// that caller's transaction instead. Append supports a producer that needs
// to store an event after the broker refuses it.
//
//   - The original TACK-336 change added this writer after the reader and
//     relay already existed. Its rationale reported an empty production
//     outbox because production did not yet write entries to that range.
func (s *OpsOutboxStore) Append(ctx context.Context, event json.RawMessage) (err error) {
	defer telemetry.FDBOp(ctx, "store.ops_outbox.append")(&err)
	if len(event) == 0 {
		err := errors.New("ops outbox append: empty event")
		slog.ErrorContext(ctx, "ops_outbox.append_empty", slog.String("err", err.Error()))
		return err
	}
	key, packErr := marshalOpsOutboxVersionstampedKey()
	if packErr != nil {
		slog.ErrorContext(ctx, "ops_outbox.append_pack_failed", slog.String("err", packErr.Error()))
		return fmt.Errorf("ops outbox append key: %w", packErr)
	}
	return commitOutboxEntries(ctx, s.db, func(tr fdb.Transaction) error {
		tr.SetVersionstampedKey(key, event)
		return nil
	})
}

// AppendBatch commits events through one OutboxWriter in one transaction.
//
// The writer preserves slice order. Every event shares the transaction's
// commit version, and increasing user versions order entries within it.
//
//   - AppendBatch returns after the transaction commits.
func (s *OpsOutboxStore) AppendBatch(ctx context.Context, events []json.RawMessage) (err error) {
	defer telemetry.FDBOp(ctx, "store.ops_outbox.append_batch")(&err)
	if len(events) == 0 {
		err := errors.New("outbox batch append did not receive any events")
		slog.ErrorContext(ctx, "ops_outbox.append_batch_empty", slog.String("err", err.Error()))
		return err
	}
	return commitOutboxEntries(ctx, s.db, func(tr fdb.Transaction) error {
		writer := NewOutboxWriter(tr)
		for _, event := range events {
			if err := writer.WriteEventJSON(ctx, event); err != nil {
				return err
			}
		}
		return nil
	})
}

// commitOutboxEntries runs apply in a transaction and commits it. It retries
// retryable FoundationDB errors and runs apply again on each attempt. The
// context deadline sets the transaction timeout, and the end of the context
// cancels the transaction.
func commitOutboxEntries(ctx context.Context, db fdb.Database, apply func(fdb.Transaction) error) error {
	// Transact requires a callback that returns an empty interface. The
	// repository's staticcheck rule rejects new empty interfaces. This mutation
	// uses an explicit transaction retry loop instead. Each retry reapplies the
	// mutation because OnError resets the transaction.
	transaction, transactionErr := db.CreateTransaction()
	if transactionErr != nil {
		slog.ErrorContext(ctx, "ops_outbox.append_transaction_failed",
			slog.String("err", transactionErr.Error()))
		return fmt.Errorf("create ops outbox append transaction: %w", transactionErr)
	}
	defer transaction.Cancel()
	// Commit and OnError waits do not monitor ctx. The watcher cancels the
	// transaction when ctx ends, including a context without a deadline.
	// Cancellation unblocks a waiting Get with an error. Cancel permits
	// repeated calls, including the deferred call after a successful commit.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "ops_outbox.append_watch_panic",
					slog.Any("panic", recovered),
					slog.String("err", fmt.Sprintf("%v", recovered)))
			}
		}()
		select {
		case <-ctx.Done():
			transaction.Cancel()
		case <-watchDone:
		}
	}()
	// The caller's deadline sets the transaction timeout. This bounds a commit
	// waiting for an unavailable cluster by the caller's remaining time. The
	// retry loop checks ctx before OnError, and the watcher cancels a blocked
	// wait when ctx ends. A Kafka failure followed by a retryable FoundationDB
	// error must not extend the call beyond its deadline.
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		budget := max(deadline.Sub(clock.Now()).Milliseconds(), 1)
		if optionErr := transaction.Options().SetTimeout(budget); optionErr != nil {
			// Return an error if the timeout option fails. Continuing would use
			// FoundationDB's default timeout instead of the caller's remaining time.
			slog.ErrorContext(ctx, "ops_outbox.append_timeout_option_failed",
				slog.Int64("budget_ms", budget),
				slog.String("err", optionErr.Error()))
			return fmt.Errorf("set ops outbox append timeout %dms: %w", budget, optionErr)
		}
	}
	for {
		if applyErr := apply(transaction); applyErr != nil {
			slog.ErrorContext(ctx, "ops_outbox.append_apply_failed", slog.String("err", applyErr.Error()))
			return fmt.Errorf("prepare ops outbox entries: %w", applyErr)
		}
		commitErr := transaction.Commit().Get()
		if commitErr == nil {
			return nil
		}
		var fdbErr fdb.Error
		if !errors.As(commitErr, &fdbErr) {
			slog.ErrorContext(ctx, "ops_outbox.append_failed", slog.String("err", commitErr.Error()))
			return fmt.Errorf("append ops outbox event: %w", commitErr)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			slog.ErrorContext(ctx, "ops_outbox.append_abandoned",
				slog.String("commit_err", commitErr.Error()),
				slog.String("err", ctxErr.Error()))
			return fmt.Errorf("append ops outbox event: %w", errors.Join(ctxErr, commitErr))
		}
		if retryErr := transaction.OnError(fdbErr).Get(); retryErr != nil {
			slog.ErrorContext(ctx, "ops_outbox.append_retry_failed",
				slog.String("err", retryErr.Error()))
			return fmt.Errorf("retry append ops outbox event: %w", retryErr)
		}
	}
}
