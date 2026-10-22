package foundationdb

import (
	"context"
	"fmt"
	"log/slog"
	"math"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"

	"goodkind.io/tack/internal/auditintent"
)

// marshalOpsOutboxVersionstampedKey packs an outbox key with user version 0.
// FoundationDB fills the versionstamp at commit, and entries sort by the
// commit that wrote them.
func marshalOpsOutboxVersionstampedKey() (fdb.Key, error) {
	return marshalOpsOutboxEntryKey(0)
}

// marshalOpsOutboxEntryKey packs an outbox key with the user version
// userVersion. Entries of one transaction sort by user version.
func marshalOpsOutboxEntryKey(userVersion uint16) (fdb.Key, error) {
	key, err := tuple.Tuple{keyOpsOutbox, tuple.IncompleteVersionstamp(userVersion)}.PackWithVersionstamp(testPrefix)
	if err != nil {
		slog.Error("ops_outbox.key_pack_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("ops outbox versionstamped key: %w", err)
	}
	return fdb.Key(key), nil
}

// writeOutboxEntries writes each value as one outbox entry in tr, with user
// versions 0, 1, 2 in slice order. Each transaction runs this function at
// most once.
func writeOutboxEntries(ctx context.Context, tr fdb.Transaction, values [][]byte) error {
	if len(values) > math.MaxUint16 {
		err := fmt.Errorf("write %d ops outbox entries: one transaction writes at most %d", len(values), math.MaxUint16)
		slog.ErrorContext(ctx, "ops_outbox.entries_failed", slog.String("err", err.Error()))
		return err
	}
	for position, value := range values {
		key, err := marshalOpsOutboxEntryKey(uint16(position))
		if err != nil {
			slog.ErrorContext(ctx, "ops_outbox.entries_failed", slog.String("err", err.Error()))
			return fmt.Errorf("ops outbox entry key %d: %w", position, err)
		}
		tr.SetVersionstampedKey(key, value)
	}
	return nil
}

// commitStagedIntent marks the request slot committed after a transaction
// wrote change events. The MCP wrapper then does not record the change a
// second time.
func commitStagedIntent(ctx context.Context) {
	auditintent.Commit(ctx)
}
