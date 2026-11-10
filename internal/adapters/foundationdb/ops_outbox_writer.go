package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"

	"github.com/apple/foundationdb/bindings/go/src/fdb"

	"goodkind.io/tack/internal/audit"
)

// outboxEntriesPerTransaction is the number of distinct two-byte user versions.
const outboxEntriesPerTransaction = math.MaxUint16 + 1

// OutboxWriter assigns increasing user versions to outbox entries in write order.
// Every outbox write in one transaction attempt must share this writer.
// FoundationDB replaces the earlier entry without an error if two entries
// in that transaction use the same user version. The relay reads entries
// in user version order.
type OutboxWriter struct {
	tr      fdb.Transaction
	next    uint16
	written int
}

// NewOutboxWriter creates a writer for tr with the next user version at zero.
// Callers create a new writer inside each transaction attempt, including retries.
func NewOutboxWriter(tr fdb.Transaction) *OutboxWriter {
	return &OutboxWriter{tr: tr, next: 0, written: 0}
}

// WriteEncodedEvent writes the records of encoded to the outbox.
// It writes plain event JSON or, for a multipart event, parts followed
// by the manifest message.
func (w *OutboxWriter) WriteEncodedEvent(ctx context.Context, encoded audit.EncodedEvent) error {
	records, err := encodedRecords(ctx, encoded)
	if err != nil {
		return err
	}
	return w.write(ctx, records)
}

// WriteEventJSON writes one plain event JSON as one outbox entry.
func (w *OutboxWriter) WriteEventJSON(ctx context.Context, event json.RawMessage) error {
	return w.write(ctx, [][]byte{event})
}

// write logs and rejects a write that would exceed outboxEntriesPerTransaction.
// It stores each value as one versionstamped entry with the next user version.
// FoundationDB fills each key's transaction version at commit.
func (w *OutboxWriter) write(ctx context.Context, values [][]byte) error {
	if w.written+len(values) > outboxEntriesPerTransaction {
		err := fmt.Errorf("ops outbox transaction would contain %d entries, more than %d", w.written+len(values), outboxEntriesPerTransaction)
		slog.ErrorContext(ctx, "ops_outbox.entry_limit_exceeded", slog.String("err", err.Error()))
		return err
	}
	for _, value := range values {
		key, err := marshalOpsOutboxEntryKey(w.next)
		if err != nil {
			slog.ErrorContext(ctx, "ops_outbox.intent_key_failed", slog.String("err", err.Error()))
			return fmt.Errorf("create ops outbox intent key: %w", err)
		}
		w.tr.SetVersionstampedKey(key, value)
		w.next++
		w.written++
	}
	return nil
}
