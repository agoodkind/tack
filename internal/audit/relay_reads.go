package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// publishedRowDeleter deletes a delivered YugabyteDB outbox row and marks
// matching read receipts as published in one transaction.
type publishedRowDeleter interface {
	DeletePublished(ctx context.Context, eventID uuid.UUID, reads []PublishedRead) error
}

// publishedEntryClearer clears delivered FoundationDB outbox entries and
// marks matching read receipts as published in one transaction.
type publishedEntryClearer interface {
	ClearThroughPublished(ctx context.Context, mark []byte, reads []PublishedRead) error
}

// deleteRow deletes a delivered row and, when the outbox supports it,
// marks the matching read receipt as published in the same transaction.
func (r *Relay) deleteRow(ctx context.Context, row OutboxRow) error {
	var err error
	if deleter, ok := r.yugabyte.(publishedRowDeleter); ok {
		err = deleter.DeletePublished(ctx, row.EventID, rowPublishedReads(ctx, row))
	} else {
		err = r.yugabyte.Delete(ctx, row.EventID)
	}
	if err != nil {
		slog.ErrorContext(ctx, "audit.relay.row_delete_failed", slog.String("event_id", row.EventID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("delete delivered outbox row %s: %w", row.EventID, err)
	}
	return nil
}

// clearThrough clears delivered entries through mark and, when the outbox
// supports it, marks their read receipts as published in the same transaction.
func (r *Relay) clearThrough(ctx context.Context, mark []byte, reads []PublishedRead) error {
	var err error
	if clearer, ok := r.foundationDB.(publishedEntryClearer); ok {
		err = clearer.ClearThroughPublished(ctx, mark, reads)
	} else {
		err = r.foundationDB.ClearThrough(ctx, mark)
	}
	if err != nil {
		slog.ErrorContext(ctx, "audit.relay.entries_clear_failed", slog.String("err", err.Error()))
		return fmt.Errorf("clear delivered outbox entries: %w", err)
	}
	return nil
}

// rowPublishedReads returns the event ID and digest for a read in a YugabyteDB row.
func rowPublishedReads(ctx context.Context, row OutboxRow) []PublishedRead {
	if len(row.Messages) > 0 {
		return recordPublishedReads(ctx, row.Messages[len(row.Messages)-1])
	}
	return eventPublishedReads(ctx, row.Event)
}

// recordPublishedReads returns an event ID and transport digest from a manifest
// or computes the digest of a plain read event. It returns nil for part records.
// If decoding fails, it returns nil and leaves the receipt unpublished.
func recordPublishedReads(ctx context.Context, raw []byte) []PublishedRead {
	message, isTransport, err := decodeTransportMessage(raw)
	if err != nil {
		slog.ErrorContext(ctx, "audit.relay.read_receipt_decode_failed", slog.String("err", err.Error()))
		return nil
	}
	if isTransport {
		if message.Message != messageKindManifest {
			return nil
		}
		return []PublishedRead{{EventID: message.EventID, Digest: message.TransportDigest}}
	}
	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		slog.ErrorContext(ctx, "audit.relay.read_receipt_decode_failed", slog.String("err", err.Error()))
		return nil
	}
	return eventPublishedReads(ctx, event)
}

// eventPublishedReads returns the event ID and digest for a plain read event.
func eventPublishedReads(ctx context.Context, event Event) []PublishedRead {
	if event.Context.OperationClass != OperationRead {
		return nil
	}
	encoded, err := EncodeEvent(event, nil)
	if err != nil {
		slog.ErrorContext(ctx, "audit.relay.read_event_encode_failed",
			slog.String("event_id", event.EventID.String()), slog.String("err", err.Error()))
		return nil
	}
	return []PublishedRead{{EventID: event.EventID, Digest: encoded.TransportDigest}}
}
