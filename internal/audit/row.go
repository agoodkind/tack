package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// Row is a flattened audit row sized for the MCP tool surface. The json tags
// repeat the field names on purpose: an export bundle is a file of these
// rows, and every bundle already written carries these exact keys, so the
// tags pin the format rather than change it.
//
// Context, Delta, and Error decode into the same types the writer marshalled,
// so verification re-canonicalizes them to the bytes the hash covered. Extra
// stays raw: it is verb-specific (operator correlation, backfill
// reconstruction evidence, and whatever a future emitter adds), and a fixed
// struct would drop keys it does not know and make honest rows fail their
// hash check.
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
	// Delta is nil for read verbs and for rows whose writer recorded no delta.
	Delta *Delta `json:"Delta"`
	// Error is nil when the action carried no error.
	Error          *EventError     `json:"Error"`
	Extra          json.RawMessage `json:"Extra"`
	PIIRef         *uuid.UUID      `json:"PIIRef"`
	PrevHash       []byte          `json:"PrevHash"`
	RowHash        []byte          `json:"RowHash"`
	HashVersion    int16           `json:"HashVersion"`
	IdempotencyKey string          `json:"IdempotencyKey"`
}

// rowPayloadTarget is the set of typed payloads a stored JSON column decodes
// into.
type rowPayloadTarget interface {
	*EventContext | **Delta | **EventError
}

// unmarshalRowPayload decodes one stored JSON payload strictly: a key the
// type does not declare is an error, never silently dropped. The row hash
// covers the stored bytes, and the verifier re-marshals the typed value, so a
// dropped key would let a planted field ride through as hash-valid history.
// Every writer since the ledger's first row marshalled exactly these types,
// so an honest row never carries an unknown key.
func unmarshalRowPayload[T rowPayloadTarget](eventID uuid.UUID, column string, raw []byte, dst T) error {
	if len(raw) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		slog.Error("audit.row.payload_decode_failed",
			slog.String("event_id", eventID.String()), slog.String("column", column), slog.String("err", err.Error()))
		return fmt.Errorf("audit row %s %s: %w", eventID, column, err)
	}
	return nil
}

// unmarshalRowPayloads decodes the context, delta, and error columns of one
// row. Extra is kept as read.
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
