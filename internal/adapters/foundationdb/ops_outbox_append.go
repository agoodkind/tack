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

// Append commits one audit event in a separate transaction under a
// versionstamped key. The relay reads events in commit order. Producers use
// this method to store events after broker failures. Callers that require
// atomic mutation evidence must append inside their mutation transaction.
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

	return commitOutboxEntries(
		ctx,
		s.db,
		func(tr fdb.Transaction) error {
			tr.SetVersionstampedKey(key, event)
			return nil
		},
	)
}

// AppendBatch writes events to the outbox through one OutboxWriter in one
// transaction in slice order. All events share one commit version and have
// increasing user versions. AppendBatch returns after the transaction commits.
func (s *OpsOutboxStore) AppendBatch(ctx context.Context, events []json.RawMessage) (err error) {
	defer telemetry.FDBOp(ctx, "store.ops_outbox.append_batch")(&err)
	if len(events) == 0 {
		err := errors.New("outbox batch append did not receive any events")
		slog.ErrorContext(ctx, "ops_outbox.append_batch_empty", slog.String("err", err.Error()))

		return err
	}

	return commitOutboxEntries(
		ctx,
		s.db,
		func(tr fdb.Transaction) error {
			writer := NewOutboxWriter(tr)

			for _, event := range events {
				if err := writer.WriteEventJSON(ctx, event); err != nil {
					return err
				}
			}

			return nil
		},
	)
}

// commitOutboxEntries runs apply in a transaction and commits it. It retries
// retryable FoundationDB errors and runs apply again on each attempt. The
// context deadline sets the transaction timeout, and the end of the context
// cancels the transaction.
func commitOutboxEntries(
	ctx context.Context,
	db fdb.Database,
	apply func(fdb.Transaction) error,
) error {
	// This explicit retry loop avoids db.Transact's required empty-interface
	// return type, which repository static checks prohibit adding. OnError
	// resets the transaction before the loop reapplies mutations.
	transaction, transactionErr := db.CreateTransaction()
	if transactionErr != nil {
		slog.ErrorContext(
			ctx,
			"ops_outbox.append_transaction_failed",
			slog.String("err", transactionErr.Error()),
		)

		return fmt.Errorf("create ops outbox append transaction: %w", transactionErr)
	}
	defer transaction.Cancel()
	// The watcher cancels blocked Commit and OnError waits when ctx ends,
	// including contexts without deadlines. Cancel also permits repeated calls
	// from deferred cleanup after commit.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(
					ctx,
					"ops_outbox.append_watch_panic",
					slog.Any("panic", recovered),
					slog.String("err", fmt.Sprintf("%v", recovered)),
				)
			}
		}()
		select {
		case <-ctx.Done():
			transaction.Cancel()
		case <-watchDone:
		}
	}()
	// Match the transaction timeout to the caller's remaining deadline.
	// The watcher also stops retry backoff when the context ends.
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		budget := max(deadline.Sub(clock.Now()).Milliseconds(), 1)
		if optionErr := transaction.Options().SetTimeout(budget); optionErr != nil {
			// Reject setup failures rather than using FoundationDB's default
			// timeout in place of the caller's deadline.
			slog.ErrorContext(
				ctx,
				"ops_outbox.append_timeout_option_failed",
				slog.Int64("budget_ms", budget),
				slog.String("err", optionErr.Error()),
			)

			return fmt.Errorf("set ops outbox append timeout %dms: %w", budget, optionErr)
		}
	}

	for {
		if applyErr := apply(transaction); applyErr != nil {
			slog.ErrorContext(
				ctx,
				"ops_outbox.append_apply_failed",
				slog.String("err", applyErr.Error()),
			)

			return fmt.Errorf("prepare ops outbox entries: %w", applyErr)
		}

		commitErr := transaction.Commit().Get()
		if commitErr == nil {
			return nil
		}

		var fdbErr fdb.Error
		if !errors.As(commitErr, &fdbErr) {
			slog.ErrorContext(
				ctx,
				"ops_outbox.append_failed",
				slog.String("err", commitErr.Error()),
			)

			return fmt.Errorf("append ops outbox event: %w", commitErr)
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			slog.ErrorContext(
				ctx,
				"ops_outbox.append_abandoned",
				slog.String("commit_err", commitErr.Error()),
				slog.String("err", ctxErr.Error()),
			)

			return fmt.Errorf("append ops outbox event: %w", errors.Join(ctxErr, commitErr))
		}

		if retryErr := transaction.OnError(fdbErr).Get(); retryErr != nil {
			slog.ErrorContext(
				ctx,
				"ops_outbox.append_retry_failed",
				slog.String("err", retryErr.Error()),
			)

			return fmt.Errorf("retry append ops outbox event: %w", retryErr)
		}
	}
}
