package foundationdb

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"

	"goodkind.io/tack/internal/auditintent"
)

// marshalOpsOutboxVersionstampedKey packs an outbox key whose versionstamp
// FoundationDB fills at commit, so entries order by the commit that wrote
// them. The packer appends the four-byte offset the key needs.
func marshalOpsOutboxVersionstampedKey() (fdb.Key, error) {
	key, err := tuple.Tuple{keyOpsOutbox, tuple.IncompleteVersionstamp(0)}.PackWithVersionstamp(testPrefix)
	if err != nil {
		slog.Error("ops_outbox.key_pack_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("ops outbox versionstamped key: %w", err)
	}
	return fdb.Key(key), nil
}

// writeStagedIntent puts the audit event staged on ctx into the outbox inside
// tr, so it commits with the change tr carries or not at all (TACK-173). A
// context with nothing staged writes nothing. Called from inside a Transact
// closure, which may run more than once; the staged event stays pending until
// commitStagedIntent, so every retry writes it again.
func writeStagedIntent(ctx context.Context, tr fdb.Transaction) error {
	payload, ok := auditintent.Pending(ctx)
	if !ok {
		return nil
	}
	key, err := marshalOpsOutboxVersionstampedKey()
	if err != nil {
		slog.ErrorContext(ctx, "ops_outbox.intent_key_failed", slog.String("err", err.Error()))
		return fmt.Errorf("ops outbox intent key: %w", err)
	}
	tr.SetVersionstampedKey(key, payload)
	return nil
}

// commitStagedIntent marks the staged event written after its transaction
// committed. The wrapper that staged it then knows not to record the same
// event a second time.
func commitStagedIntent(ctx context.Context) {
	auditintent.Commit(ctx)
}
