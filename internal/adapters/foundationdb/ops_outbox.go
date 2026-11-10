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

// OpsOutboxStore reads and clears audit outbox entries in FoundationDB. A
// command appends its change event in the same transaction as its data
// change. The change and outbox event commit together. The relay in the
// audit-consumer reads and publishes the entries before clearing them.
type OpsOutboxStore struct {
	db fdb.Database
}

// NewOpsOutboxStore creates an operator command audit outbox store.
//
// The returned store uses db for outbox operations.
//
//   - ReadOutboxFrom reads committed entries.
//   - ClearThrough deletes delivered entries.
func NewOpsOutboxStore(db fdb.Database) *OpsOutboxStore {
	return &OpsOutboxStore{db: db}
}

// OutboxEntry is one operator-command audit event waiting for relay
// delivery.
type OutboxEntry struct {
	// Mark stores the raw versionstamped key. It orders entries by commit and
	// sets the inclusive endpoint for a later ClearThrough call.
	Mark []byte
	// Event stores the audit event JSON payload. This store copies the payload
	// without decoding it. The audit package decodes its event format during
	// relay publication.
	Event json.RawMessage
	// CommitVersion is the versionstamp in Mark as 24 hexadecimal characters.
	// Its lexical order is the commit order of the entries.
	CommitVersion string
}

// ReadOutboxFrom reads up to limit events after mark in commit order.
//
// It validates a nonempty mark before reading the outbox range.
//
//   - A nil mark starts at the beginning of the outbox.
func (s *OpsOutboxStore) ReadOutboxFrom(
	ctx context.Context,
	mark []byte,
	limit int,
) (entries []OutboxEntry, err error) {
	defer telemetry.FDBOp(ctx, "store.ops_outbox.read")(&err)

	outboxRange, rangeErr := fdb.PrefixRange(opsOutboxPrefix())
	if rangeErr != nil {
		slog.ErrorContext(
			ctx,
			"ops_outbox.read_prefix_failed",
			slog.String("err", rangeErr.Error()),
		)

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

	// ReadTransact requires a callback that returns an empty interface. The
	// repository's staticcheck rule rejects new empty interfaces. This read
	// uses an explicit transaction retry loop instead.
	transaction, transactionErr := s.db.CreateTransaction()
	if transactionErr != nil {
		slog.ErrorContext(
			ctx,
			"ops_outbox.read_transaction_failed",
			slog.String("err", transactionErr.Error()),
		)

		return nil, fmt.Errorf("create ops outbox read transaction: %w", transactionErr)
	}
	defer transaction.Cancel()

	for {
		keyValues, readErr := transaction.
			GetRange(
				readRange,
				fdb.RangeOptions{Limit: limit},
			).
			GetSliceWithError()
		if readErr == nil {
			return outboxEntries(ctx, keyValues)
		}

		var fdbErr fdb.Error
		if !errors.As(readErr, &fdbErr) {
			slog.ErrorContext(ctx, "ops_outbox.read_failed", slog.String("err", readErr.Error()))
			return nil, fmt.Errorf("read ops outbox: %w", readErr)
		}

		if retryErr := transaction.OnError(fdbErr).Get(); retryErr != nil {
			slog.ErrorContext(
				ctx,
				"ops_outbox.read_retry_failed",
				slog.String("err", retryErr.Error()),
			)

			return nil, fmt.Errorf("retry read ops outbox: %w", retryErr)
		}
	}
}

// outboxEntries decodes each key's commit version and copies each key and
// value into an OutboxEntry. The caller must not retain the transaction's
// buffers after the transaction ends.
func outboxEntries(ctx context.Context, keyValues []fdb.KeyValue) ([]OutboxEntry, error) {
	entries := make([]OutboxEntry, 0, len(keyValues))
	for _, keyValue := range keyValues {
		commitVersion, err := outboxCommitVersion(ctx, keyValue.Key)
		if err != nil {
			return nil, err
		}

		entries = append(
			entries,
			OutboxEntry{
				Mark:          append([]byte(nil), keyValue.Key...),
				Event:         append(json.RawMessage(nil), keyValue.Value...),
				CommitVersion: commitVersion,
			},
		)
	}

	return entries, nil
}

// ClearThrough deletes every outbox entry up to and including mark.
//
// The relay clears entries after the broker acknowledges them. Deletion
// marks delivery without a separate key for the last delivered entry.
//
//   - ClearThrough does not delete entries when mark is empty.
func (s *OpsOutboxStore) ClearThrough(ctx context.Context, mark []byte) (err error) {
	defer telemetry.FDBOp(ctx, "store.ops_outbox.clear_through")(&err)
	if len(mark) == 0 {
		return nil
	}

	// A mark outside the outbox family can extend the clear range into
	// unrelated key families and delete product data. The relay obtains its
	// mark from an outbox read. Reject an invalid mark and preserve the batch
	// for later delivery.
	if err := checkOutboxMark(ctx, mark); err != nil {
		return err
	}

	// Strinc returns the first key outside the range with mark as its prefix.
	// This exclusive endpoint includes mark in the cleared range.
	end, endErr := fdb.Strinc(mark)
	if endErr != nil {
		slog.ErrorContext(ctx, "ops_outbox.clear_end_failed", slog.String("err", endErr.Error()))
		return fmt.Errorf("ops outbox clear end key: %w", endErr)
	}

	clearRange := fdb.KeyRange{Begin: fdb.Key(opsOutboxPrefix()), End: fdb.Key(end)}

	// Transact requires a callback that returns an empty interface. The
	// repository's staticcheck rule rejects new empty interfaces. This mutation
	// uses an explicit transaction retry loop instead.
	transaction, transactionErr := s.db.CreateTransaction()
	if transactionErr != nil {
		slog.ErrorContext(
			ctx,
			"ops_outbox.clear_transaction_failed",
			slog.String("err", transactionErr.Error()),
		)

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
			slog.ErrorContext(
				ctx,
				"ops_outbox.clear_retry_failed",
				slog.String("err", retryErr.Error()),
			)

			return fmt.Errorf("retry clear ops outbox through mark: %w", retryErr)
		}
	}
}

// checkOutboxMark rejects marks outside the outbox key family. The read and
// clear operations use marks as range bounds. Rejecting an invalid mark
// prevents ClearThrough from deleting product data outside the outbox.
func checkOutboxMark(ctx context.Context, mark []byte) error {
	if bytes.HasPrefix(mark, opsOutboxPrefix()) {
		return nil
	}

	err := fmt.Errorf("ops outbox mark is outside the outbox key family")
	slog.ErrorContext(
		ctx,
		"ops_outbox.mark_out_of_family",
		slog.Int("mark_len", len(mark)),
		slog.String("err", err.Error()),
	)

	return err
}
