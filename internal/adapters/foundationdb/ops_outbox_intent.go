package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"

	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/domain/node"
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

// writeStagedIntent writes the staged audit event into the outbox in tr for a
// change without a node primary record, such as a relationship change.
func writeStagedIntent(ctx context.Context, tr fdb.Transaction) error {
	return writeMutationIntent(ctx, tr, nil, nil)
}

// writeNodeIntent writes the staged audit event for a node change. source
// contains the primary record encoding that tr read; a nil or empty encoding
// indicates an absent record. result is the node that tr wrote.
func writeNodeIntent(ctx context.Context, tr fdb.Transaction, source []byte, result *node.Node) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		slog.ErrorContext(ctx, "ops_outbox.intent_encode_failed", slog.String("node_id", result.ID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("encode node %s for its audit event: %w", result.ID, err)
	}
	return writeMutationIntent(ctx, tr, source, encoded)
}

// writeMutationIntent writes the audit event staged on ctx into the outbox in
// tr, atomically with the change (TACK-173). Each attempt builds a new event
// from the unchanged staged template and source and result, the primary
// record encodings that the attempt read and wrote. A nil or empty encoding
// indicates an absent record, and the template remains staged until
// commitStagedIntent runs. A context without a staged event does not write an
// outbox entry.
func writeMutationIntent(ctx context.Context, tr fdb.Transaction, source, result []byte) error {
	payload, ok, err := auditintent.Build(ctx, source, result)
	if err != nil {
		slog.ErrorContext(ctx, "ops_outbox.intent_build_failed", slog.String("err", err.Error()))
		return fmt.Errorf("build the ops outbox intent: %w", err)
	}
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
