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

// Row defines the flattened record for exports and operator reads. Preserve
// the JSON tag spelling to match existing bundles. Context, Delta, and Error
// use the writer's types for hash verification. Extra remains raw because
// verb-specific keys must survive decoding and canonicalization.
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
	// Delta is nil for reads and when the writer did not record a delta.
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

// readAuditRow scans audit.events columns in the canonical ledger SELECT order.
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

// rowPayloadTarget defines the destination types for stored JSON columns.
type rowPayloadTarget interface {
	*EventContext | **Delta | **EventError
}

// unmarshalRowPayload rejects JSON keys that the destination type does not
// declare. The verifier re-marshals typed values to check hashes. Dropping an
// unknown key would hide an injected field from that check. Writers use these
// same types and do not produce unknown keys.
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

// unmarshalRowPayloads decodes context, delta, and error and retains raw Extra.
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
