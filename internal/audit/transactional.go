package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// BatchOutboxWriter commits all events for a request in one transaction.
type BatchOutboxWriter interface {
	WriteOutboxBatch(ctx context.Context, events []Event) error
}

// OutboxBatchAppender commits encoded events together in slice order.
type OutboxBatchAppender interface {
	AppendBatch(ctx context.Context, events []json.RawMessage) error
}

// BatchAppendOutbox encodes request events for a transactional outbox.
type BatchAppendOutbox struct {
	appender OutboxBatchAppender
}

// NewBatchAppendOutbox uses appender for each request batch.
//
// Each request batch includes one event per organization.
//
//	outbox := NewBatchAppendOutbox(stores.OpsOutbox)
func NewBatchAppendOutbox(appender OutboxBatchAppender) BatchAppendOutbox {
	return BatchAppendOutbox{appender: appender}
}

// WriteOutboxBatch commits the events in slice order.
//
// If any event exceeds the outbox record size, it rejects the whole batch.
//
//	err := outbox.WriteOutboxBatch(ctx, events)
func (outbox BatchAppendOutbox) WriteOutboxBatch(ctx context.Context, events []Event) error {
	records := make([]json.RawMessage, 0, len(events))
	for _, event := range events {
		encoded, err := MarshalEvent(event)
		if err != nil {
			slog.ErrorContext(ctx, "audit.request.encode_failed", slog.Any("err", err))

			return fmt.Errorf("encode request event %s: %w", event.EventID, err)
		}

		if !FitsSingleRecord(encoded) {
			return fmt.Errorf(
				"%w: request event %s exceeds the outbox record size",
				ErrEventNotRecorded,
				event.EventID,
			)
		}

		records = append(records, encoded)
	}

	if err := outbox.appender.AppendBatch(ctx, records); err != nil {
		slog.ErrorContext(ctx, "audit.request.append_failed", slog.Any("err", err))

		return fmt.Errorf("append request events: %w", err)
	}

	return nil
}
