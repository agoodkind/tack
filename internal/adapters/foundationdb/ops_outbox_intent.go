package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/domain/node"
)

// marshalOpsOutboxVersionstampedKey packs an outbox key whose versionstamp
// FoundationDB fills at commit, so entries order by the commit that wrote
// them. The packer appends the four-byte offset the key needs.
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

// writeMutationIntent writes the event staged on ctx with a new OutboxWriter
// in tr, in the same transaction as the change. Callers use it when their
// transaction writes a single outbox event. A transaction that writes several
// outbox events must create one OutboxWriter per attempt and pass it to every
// write.
//
// Each attempt builds a new event from the unchanged staged template, the
// source encoding it read, and the result encoding it wrote for the primary
// record. A nil or empty encoding indicates an absent record.
//
// The template remains staged until commitStagedIntent runs. A context without
// a staged event does not write an outbox entry.
func writeMutationIntent(ctx context.Context, tr fdb.Transaction, source, result []byte) error {
	return appendMutationIntent(ctx, NewOutboxWriter(tr), source, result)
}

// appendMutationIntent builds the event staged on ctx for one transaction
// attempt and writes it with outbox.
func appendMutationIntent(ctx context.Context, outbox *OutboxWriter, source, result []byte) error {
	payload, ok, err := auditintent.Build(ctx, source, result)
	if err != nil {
		slog.ErrorContext(ctx, "ops_outbox.intent_build_failed", slog.String("err", err.Error()))
		return fmt.Errorf("build the ops outbox intent: %w", err)
	}
	if !ok {
		return nil
	}
	entries, err := outboxEntryValues(ctx, payload)
	if err != nil {
		return err
	}
	return outbox.write(ctx, entries)
}

// outboxEntryValues keeps payloads within EventPartMaxBytes as one plain entry.
// It decodes larger payloads as audit events and returns their outbox records.
func outboxEntryValues(ctx context.Context, payload json.RawMessage) ([][]byte, error) {
	if audit.FitsSingleRecord(payload) {
		return [][]byte{payload}, nil
	}
	var event audit.Event
	if err := json.Unmarshal(payload, &event); err != nil {
		slog.ErrorContext(ctx, "ops_outbox.intent_decode_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("decode the ops outbox intent: %w", err)
	}
	encoded, err := audit.EncodeEvent(event, nil)
	if err != nil {
		slog.ErrorContext(ctx, "ops_outbox.intent_parts_failed", slog.String("event_id", event.EventID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("split the ops outbox intent %s into parts: %w", event.EventID, err)
	}
	if !encoded.Multipart() {
		return [][]byte{payload}, nil
	}
	return encodedRecords(ctx, encoded)
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

// commitStagedIntent marks the staged event written after its transaction
// committed. The wrapper that staged it then knows not to record the same
// event a second time.
func commitStagedIntent(ctx context.Context) {
	auditintent.Commit(ctx)
}
