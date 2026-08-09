package foundationdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/telemetry"
)

// OpsOutboxStore reads and clears operator-command audit events in
// FoundationDB. A command appends its event inside the same transaction as
// the change it makes, so the change and its ledger record commit together;
// the relay in the audit-consumer drains what this store returns.
type OpsOutboxStore struct {
	db fdb.Database
}

// NewOpsOutboxStore returns an operator-command audit outbox store over db.
func NewOpsOutboxStore(db fdb.Database) *OpsOutboxStore {
	return &OpsOutboxStore{db: db}
}

// OutboxEntry is one operator-command audit event waiting for relay delivery.
type OutboxEntry struct {
	// Mark is the raw versionstamped key. It orders entries by commit and
	// names the point a later ClearThrough deletes up to.
	Mark []byte
	// Event is the audit event JSON payload. This package does not decode it,
	// because the audit package owns the event shape and importing it here
	// would invert the dependency.
	Event json.RawMessage
}

// ReadOutboxFrom reads up to limit events committed after mark, in commit
// order. A nil mark starts at the beginning of the outbox.
func (s *OpsOutboxStore) ReadOutboxFrom(
	ctx context.Context,
	mark []byte,
	limit int,
) (entries []OutboxEntry, err error) {
	defer telemetry.FDBOp(ctx, "store.ops_outbox.read")(&err)

	outboxRange, rangeErr := fdb.PrefixRange(opsOutboxPrefix())
	if rangeErr != nil {
		slog.ErrorContext(ctx, "ops_outbox.read_prefix_failed", slog.String("err", rangeErr.Error()))
		return nil, fmt.Errorf("ops outbox prefix range: %w", rangeErr)
	}
	begin := fdb.FirstGreaterOrEqual(outboxRange.Begin)
	if len(mark) > 0 {
		if err := checkOutboxMark(ctx, mark); err != nil {
			return nil, err
		}
		begin = fdb.FirstGreaterThan(fdb.Key(mark))
	}
	readRange := fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(outboxRange.End)}

	// db.ReadTransact is unavailable here for the same reason ClearThrough
	// avoids db.Transact: its closure must return an empty interface, and the
	// repo's staticcheck rule refuses a new one. This is that retry loop
	// spelled out, over a read transaction.
	transaction, transactionErr := s.db.CreateTransaction()
	if transactionErr != nil {
		slog.ErrorContext(ctx, "ops_outbox.read_transaction_failed",
			slog.String("err", transactionErr.Error()))
		return nil, fmt.Errorf("create ops outbox read transaction: %w", transactionErr)
	}
	defer transaction.Cancel()
	for {
		keyValues, readErr := transaction.
			GetRange(readRange, fdb.RangeOptions{Limit: limit}).
			GetSliceWithError()
		if readErr == nil {
			return outboxEntries(keyValues), nil
		}
		var fdbErr fdb.Error
		if !errors.As(readErr, &fdbErr) {
			slog.ErrorContext(ctx, "ops_outbox.read_failed", slog.String("err", readErr.Error()))
			return nil, fmt.Errorf("read ops outbox: %w", readErr)
		}
		if retryErr := transaction.OnError(fdbErr).Get(); retryErr != nil {
			slog.ErrorContext(ctx, "ops_outbox.read_retry_failed",
				slog.String("err", retryErr.Error()))
			return nil, fmt.Errorf("retry read ops outbox: %w", retryErr)
		}
	}
}

// outboxEntries copies each key and value out of the transaction's buffers,
// which the caller must not hold on to once the transaction ends.
func outboxEntries(keyValues []fdb.KeyValue) []OutboxEntry {
	entries := make([]OutboxEntry, 0, len(keyValues))
	for _, keyValue := range keyValues {
		entries = append(entries, OutboxEntry{
			Mark:  append([]byte(nil), keyValue.Key...),
			Event: append(json.RawMessage(nil), keyValue.Value...),
		})
	}
	return entries
}

// ClearThrough deletes every outbox entry up to and including mark. The relay
// calls it only after the broker has acknowledged those events, so deletion
// is what marks them delivered; no separate high-water-mark key exists to
// drift out of step with the data.
func (s *OpsOutboxStore) ClearThrough(ctx context.Context, mark []byte) (err error) {
	defer telemetry.FDBOp(ctx, "store.ops_outbox.clear_through")(&err)
	if len(mark) == 0 {
		return nil
	}
	// A mark that is not an outbox key would make the cleared range run past
	// the outbox and destroy unrelated key families. The relay's mark comes
	// from a previous read, so a bad one means corrupted state, and refusing
	// costs one undelivered batch while proceeding costs other people's data.
	if err := checkOutboxMark(ctx, mark); err != nil {
		return err
	}

	// Strinc gives the first key after mark, which makes the exclusive end of
	// the cleared range include mark itself.
	end, endErr := fdb.Strinc(mark)
	if endErr != nil {
		slog.ErrorContext(ctx, "ops_outbox.clear_end_failed", slog.String("err", endErr.Error()))
		return fmt.Errorf("ops outbox clear end key: %w", endErr)
	}
	clearRange := fdb.KeyRange{Begin: fdb.Key(opsOutboxPrefix()), End: fdb.Key(end)}

	// A mutation with no result cannot go through db.Transact here: that
	// binding's closure must return an empty interface, and the repo's
	// staticcheck rule refuses a new one. The retry loop below is what
	// db.Transact does internally, spelled out.
	transaction, transactionErr := s.db.CreateTransaction()
	if transactionErr != nil {
		slog.ErrorContext(ctx, "ops_outbox.clear_transaction_failed",
			slog.String("err", transactionErr.Error()))
		return fmt.Errorf("create ops outbox clear transaction: %w", transactionErr)
	}
	defer transaction.Cancel()
	for {
		transaction.ClearRange(clearRange)
		commitErr := transaction.Commit().Get()
		if commitErr == nil {
			return nil
		}
		var fdbErr fdb.Error
		if !errors.As(commitErr, &fdbErr) {
			slog.ErrorContext(ctx, "ops_outbox.clear_failed", slog.String("err", commitErr.Error()))
			return fmt.Errorf("clear ops outbox through mark: %w", commitErr)
		}
		if retryErr := transaction.OnError(fdbErr).Get(); retryErr != nil {
			slog.ErrorContext(ctx, "ops_outbox.clear_retry_failed",
				slog.String("err", retryErr.Error()))
			return fmt.Errorf("retry clear ops outbox through mark: %w", retryErr)
		}
	}
}

// checkOutboxMark rejects a mark that does not lie inside the outbox key
// family. Both callers use the mark to bound a range, and ClearThrough
// deletes that range, so an out-of-family mark is the difference between
// clearing delivered events and deleting product data.
func checkOutboxMark(ctx context.Context, mark []byte) error {
	if bytes.HasPrefix(mark, opsOutboxPrefix()) {
		return nil
	}
	err := fmt.Errorf("ops outbox mark is outside the outbox key family")
	slog.ErrorContext(ctx, "ops_outbox.mark_out_of_family",
		slog.Int("mark_len", len(mark)),
		slog.String("err", err.Error()),
	)
	return err
}
