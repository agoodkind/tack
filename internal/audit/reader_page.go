package audit

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultQueryPageLimit is the page size when the caller asks for none.
	DefaultQueryPageLimit = 100
	// MaxQueryPageLimit caps one page; larger requests clamp down to it.
	MaxQueryPageLimit = 1000

	queryCursorSeparator = "|"
)

// Page is one page of ledger rows. NextCursor is non-empty exactly when the
// page filled, and resumes the same query at the next row.
type Page struct {
	Rows []Row
	// NextCursor is opaque to callers; pass it back through
	// QueryFilter cursor decoding via QueryPage.
	NextCursor string
}

// QueryPage runs Query with the limit clamped to [1, MaxQueryPageLimit] and
// keyset pagination behind one opaque cursor, so every surface pages the
// ledger the same way. An empty cursor starts at the newest row.
func (r *Reader) QueryPage(ctx context.Context, f QueryFilter, cursor string) (Page, error) {
	if f.Limit <= 0 {
		f.Limit = DefaultQueryPageLimit
	}
	if f.Limit > MaxQueryPageLimit {
		f.Limit = MaxQueryPageLimit
	}
	if cursor != "" {
		beforeTime, beforeSeq, err := UnmarshalQueryCursor(ctx, cursor)
		if err != nil {
			slog.ErrorContext(ctx, "audit.query_page.bad_cursor", slog.String("err", err.Error()))
			return Page{}, fmt.Errorf("audit query cursor: %w", err)
		}
		f.BeforeTime = beforeTime
		f.BeforeSeq = beforeSeq
	}
	rows, err := r.Query(ctx, f)
	if err != nil {
		return Page{}, err
	}
	page := Page{Rows: rows, NextCursor: ""}
	if len(rows) == f.Limit {
		last := rows[len(rows)-1]
		page.NextCursor = MarshalQueryCursor(last.EventTime, last.Seq)
	}
	return page, nil
}

// MarshalQueryCursor renders the (event_time, seq) resume position as one
// opaque string. Exported with UnmarshalQueryCursor so any paging surface
// shares one cursor format.
func MarshalQueryCursor(eventTime time.Time, seq int64) string {
	raw := eventTime.UTC().Format(time.RFC3339Nano) + queryCursorSeparator + strconv.FormatInt(seq, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// UnmarshalQueryCursor reverses MarshalQueryCursor. Every rejection names the
// broken part; a cursor is operator input, so the reason is logged here where
// the raw value is still known.
func UnmarshalQueryCursor(ctx context.Context, cursor string) (time.Time, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		slog.WarnContext(ctx, "audit.query_cursor.invalid", slog.String("part", "encoding"), slog.String("err", err.Error()))
		return time.Time{}, 0, fmt.Errorf("decode cursor: %w", err)
	}
	timePart, seqPart, ok := strings.Cut(string(raw), queryCursorSeparator)
	if !ok {
		slog.WarnContext(ctx, "audit.query_cursor.invalid", slog.String("part", "shape"))
		return time.Time{}, 0, errors.New("cursor is not a next_cursor from a previous page")
	}
	beforeTime, err := time.Parse(time.RFC3339Nano, timePart)
	if err != nil {
		slog.WarnContext(ctx, "audit.query_cursor.invalid", slog.String("part", "time"), slog.String("err", err.Error()))
		return time.Time{}, 0, fmt.Errorf("cursor time: %w", err)
	}
	beforeSeq, err := strconv.ParseInt(seqPart, 10, 64)
	if err != nil {
		slog.WarnContext(ctx, "audit.query_cursor.invalid", slog.String("part", "seq"), slog.String("err", err.Error()))
		return time.Time{}, 0, fmt.Errorf("cursor seq: %w", err)
	}
	return beforeTime, beforeSeq, nil
}
