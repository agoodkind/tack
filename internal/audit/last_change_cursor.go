package audit

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// historyCursorParts is the number of separated parts in a decoded history
// cursor: the event time, the sequence number, and the event ID.
const historyCursorParts = 3

// marshalHistoryCursor encodes position as one opaque string.
func marshalHistoryCursor(position historyPosition) string {
	raw := strings.Join([]string{
		position.eventTime.UTC().Format(time.RFC3339Nano),
		strconv.FormatInt(position.seq, 10),
		position.eventID.String(),
	}, queryCursorSeparator)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// unmarshalHistoryCursor decodes a cursor that marshalHistoryCursor wrote.
func unmarshalHistoryCursor(ctx context.Context, cursor string) (historyPosition, error) {
	var position historyPosition
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		slog.ErrorContext(ctx, "audit.history_cursor.invalid", slog.String("part", "encoding"), slog.String("err", err.Error()))
		return position, fmt.Errorf("decode the history cursor: %w", err)
	}
	parts := strings.Split(string(raw), queryCursorSeparator)
	if len(parts) != historyCursorParts {
		slog.ErrorContext(ctx, "audit.history_cursor.invalid", slog.String("part", "shape"), slog.String("err", errHistoryCursor.Error()))
		return position, errHistoryCursor
	}
	eventTime, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		slog.ErrorContext(ctx, "audit.history_cursor.invalid", slog.String("part", "time"), slog.String("err", err.Error()))
		return position, fmt.Errorf("history cursor time: %w", err)
	}
	seq, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		slog.ErrorContext(ctx, "audit.history_cursor.invalid", slog.String("part", "seq"), slog.String("err", err.Error()))
		return position, fmt.Errorf("history cursor seq: %w", err)
	}
	eventID, err := uuid.Parse(parts[2])
	if err != nil {
		slog.ErrorContext(ctx, "audit.history_cursor.invalid", slog.String("part", "event_id"), slog.String("err", err.Error()))
		return position, fmt.Errorf("history cursor event id: %w", err)
	}
	return historyPosition{eventTime: eventTime, seq: seq, eventID: eventID}, nil
}
