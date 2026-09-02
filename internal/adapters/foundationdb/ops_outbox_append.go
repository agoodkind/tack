package foundationdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

// Append writes one audit event to the outbox under a versionstamped key, in a
// transaction of its own, and returns once it is committed.
//
// This is the writer half of the outbox. The reader and the relay that drains
// it existed first, and until this function nothing in production wrote to the
// range they polled, so the relay had only ever drained an empty outbox. The
// versionstamp orders entries by commit, which is the order the relay delivers
// them in.
//
// A caller with a transaction of its own, such as an operator command that
// wants the event committed with the change it describes, appends inside that
// transaction instead; this method exists for the path that has no transaction
// to join, which is a producer spilling an event its broker refused.
func (s *OpsOutboxStore) Append(ctx context.Context, event json.RawMessage) (err error) {
	defer telemetry.FDBOp(ctx, "store.ops_outbox.append")(&err)
	if len(event) == 0 {
		err := errors.New("ops outbox append: empty event")
		slog.ErrorContext(ctx, "ops_outbox.append_empty", slog.String("err", err.Error()))
		return err
	}
	// The packer places the incomplete versionstamp in the key and appends
	// the four-byte offset FoundationDB needs to fill it at commit.
	key, packErr := tuple.Tuple{keyOpsOutbox, tuple.IncompleteVersionstamp(0)}.PackWithVersionstamp(testPrefix)
	if packErr != nil {
		slog.ErrorContext(ctx, "ops_outbox.append_pack_failed", slog.String("err", packErr.Error()))
		return fmt.Errorf("ops outbox append key: %w", packErr)
	}

	// A mutation with no result cannot go through db.Transact here: that
	// binding's closure must return an empty interface, and the repo's
	// staticcheck rule refuses a new one. The retry loop below is what
	// db.Transact does internally, spelled out. The mutation is re-applied on
	// every pass because OnError resets the transaction.
	transaction, transactionErr := s.db.CreateTransaction()
	if transactionErr != nil {
		slog.ErrorContext(ctx, "ops_outbox.append_transaction_failed",
			slog.String("err", transactionErr.Error()))
		return fmt.Errorf("create ops outbox append transaction: %w", transactionErr)
	}
	defer transaction.Cancel()
	// The caller's deadline becomes the transaction's own timeout, so a
	// commit blocked on an unreachable cluster fails inside the caller's
	// budget rather than FoundationDB's, and the retry loop stops when the
	// context is done rather than after the next OnError backoff. Without
	// both, a request that lost Kafka and then hit a retryable
	// FoundationDB error could sit here past its budget.
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		budget := max(deadline.Sub(clock.Now()).Milliseconds(), 1)
		if optionErr := transaction.Options().SetTimeout(budget); optionErr != nil {
			// Refusing is right here: proceeding would run on FoundationDB's
			// default timeout while the caller believes its budget holds.
			slog.ErrorContext(ctx, "ops_outbox.append_timeout_option_failed",
				slog.Int64("budget_ms", budget),
				slog.String("err", optionErr.Error()))
			return fmt.Errorf("set ops outbox append timeout %dms: %w", budget, optionErr)
		}
	}
	for {
		transaction.SetVersionstampedKey(fdb.Key(key), event)
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
