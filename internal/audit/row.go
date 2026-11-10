package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Row represents an audit row for exports and operator reads. Export bundles
// store these rows. The JSON tags preserve the exact keys from earlier
// bundles.
//
// Context, Delta, and Error decode into the writer's types. Verification
// marshals those values again to reconstruct the bytes for the row hash.
// Extra retains raw JSON because its keys depend on the verb. Examples
// include operator correlation, backfill reconstruction evidence, and fields
// that future emitters add. A fixed struct could discard unknown keys and
// make a valid row fail hash verification.
type Row struct {
	OrgID     uuid.UUID `json:"OrgID"`
	EventTime time.Time `json:"EventTime"`
	EventID   uuid.UUID `json:"EventID"`
	Seq       int64     `json:"Seq"`
	Shard     int16     `json:"Shard"`
	ActorID   uuid.UUID `json:"ActorID"`
	ActorKind int16     `json:"ActorKind"`
	Action    string    `json:"Action"`
	// Outcome records whether the action succeeded.
	Outcome    Outcome      `json:"Outcome"`
	EntityKind string       `json:"EntityKind"`
	EntityID   uuid.UUID    `json:"EntityID"`
	Context    EventContext `json:"Context"`
	// Delta is nil for read verbs and when the writer did not record a delta.
	Delta *Delta `json:"Delta"`
	// Error is nil when the action did not report an error.
	Error          *EventError     `json:"Error"`
	Extra          json.RawMessage `json:"Extra"`
	PIIRef         *uuid.UUID      `json:"PIIRef"`
	PrevHash       []byte          `json:"PrevHash"`
	RowHash        []byte          `json:"RowHash"`
	HashVersion    int16           `json:"HashVersion"`
	IdempotencyKey string          `json:"IdempotencyKey"`
	// ManifestParts contains the parts of a read event with manifest evidence.
	// JSON omits an empty slice, preserving bundle bytes for rows without parts.
	ManifestParts []ManifestPart `json:"ManifestParts,omitempty" exhaustruct:"optional"`
}

// readAuditRow scans audit.events columns in the order that ledger SELECT
// queries use:
//
//  1. The query selects org_id, event_time, event_id, seq, and shard.
//  2. It selects actor_id, actor_kind, action, outcome, entity_kind, and
//     entity_id.
//  3. It selects context, delta, error, extra, and pii_ref.
//  4. It selects prev_hash, row_hash, hash_version, and idempotency_key.
func readAuditRow(rows pgx.Rows) (Row, error) {
	var row Row
	var outcome *string
	var contextRaw, deltaRaw, errorRaw, extraRaw []byte
	if err := rows.Scan(
		&row.OrgID,
		&row.EventTime,
		&row.EventID,
		&row.Seq,
		&row.Shard,
		&row.ActorID,
		&row.ActorKind,
		&row.Action,
		&outcome,
		&row.EntityKind,
		&row.EntityID,
		&contextRaw,
		&deltaRaw,
		&errorRaw,
		&extraRaw,
		&row.PIIRef,
		&row.PrevHash,
		&row.RowHash,
		&row.HashVersion,
		&row.IdempotencyKey,
	); err != nil {
		slog.Error("audit.row.scan_failed", slog.String("err", err.Error()))
		return row, fmt.Errorf("audit row scan: %w", err)
	}

	if err := unmarshalRowPayloads(&row, contextRaw, deltaRaw, errorRaw, extraRaw); err != nil {
		return row, err
	}

	row.Outcome = outcomeFromColumn(outcome)

	return row, nil
}

// rowPayloadTarget permits the typed targets for stored JSON columns.
type rowPayloadTarget interface {
	*EventContext | **Delta | **EventError
}

// unmarshalRowPayload decodes stored JSON and rejects keys that the target
// type does not declare. Verification marshals the typed value again for the
// row hash. Discarding an unknown key could omit a planted field from that
// check.
//
// The original comment records that ledger writers used these types from the
// first row. That historical format explains the strict rejection of unknown
// fields.
func unmarshalRowPayload[T rowPayloadTarget](
	eventID uuid.UUID,
	column string,
	raw []byte,
	dst T,
) error {
	if len(raw) == 0 {
		return nil
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		slog.Error(
			"audit.row.payload_decode_failed",
			slog.String("event_id", eventID.String()),
			slog.String("column", column),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("audit row %s %s: %w", eventID, column, err)
	}

	return nil
}

// unmarshalRowPayloads decodes one row's context, delta, and error columns.
// It preserves Extra as raw JSON.
func unmarshalRowPayloads(row *Row, contextRaw, deltaRaw, errorRaw, extraRaw []byte) error {
	if err := unmarshalRowPayload(row.EventID, "context", contextRaw, &row.Context); err != nil {
		return err
	}

	if err := unmarshalRowPayload(row.EventID, "delta", deltaRaw, &row.Delta); err != nil {
		return err
	}

	if err := unmarshalRowPayload(row.EventID, "error", errorRaw, &row.Error); err != nil {
		return err
	}

	if len(extraRaw) != 0 {
		row.Extra = json.RawMessage(extraRaw)
	}

	return nil
}
