package foundationdb

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
)

// marshalOpsOutboxVersionstampedKey packs an outbox key with user version 0.
// FoundationDB fills the versionstamp at commit, and entries sort by the
// commit that wrote them.
func marshalOpsOutboxVersionstampedKey() (fdb.Key, error) {
	return marshalOpsOutboxEntryKey(0)
}

// marshalOpsOutboxEntryKey packs a versionstamped outbox key with the given user version.
func marshalOpsOutboxEntryKey(userVersion uint16) (fdb.Key, error) {
	key, err := tuple.Tuple{keyOpsOutbox, tuple.IncompleteVersionstamp(userVersion)}.PackWithVersionstamp(testPrefix)
	if err != nil {
		slog.Error("ops_outbox.key_pack_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("ops outbox versionstamped key: %w", err)
	}
	return fdb.Key(key), nil
}

// encodedRecords returns the event's outbox records and wraps a Records error
// with the event ID.
func encodedRecords(ctx context.Context, encoded audit.EncodedEvent) ([][]byte, error) {
	records, err := encoded.Records()
	if err != nil {
		slog.ErrorContext(ctx, "ops_outbox.encoded_records_failed", slog.String("event_id", encoded.EventID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("encode the ops outbox records of event %s: %w", encoded.EventID, err)
	}
	return records, nil
}

// commitStagedIntent marks the request slot committed after a transaction
// wrote change events. The MCP wrapper then does not record the change a
// second time.
func commitStagedIntent(ctx context.Context) {
	auditintent.Commit(ctx)
}
