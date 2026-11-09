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
	// DefaultQueryPageLimit is the page size when the caller omits a limit.
	DefaultQueryPageLimit = 100
	// MaxQueryPageLimit caps a page; QueryPage reduces larger limits to this value.
	MaxQueryPageLimit = 1000

	queryCursorSeparator = "|"
)

// Page contains one page of ledger rows. A full page includes a nonempty
// NextCursor that resumes the same query after its last row.
type Page struct {
	Rows []Row
	// NextCursor is an opaque cursor that QueryPage decodes into a QueryFilter.
	NextCursor string
}

// QueryPage runs [Reader.Query] with a bounded limit and an opaque cursor.
//
// All callers use the same keyset pagination, which resumes after the last
// row's event time and sequence. An empty cursor starts at the newest row
// when f does not already specify a cursor position.
//
//   - A nonpositive limit uses DefaultQueryPageLimit.
//   - A limit above MaxQueryPageLimit uses MaxQueryPageLimit.
//   - A full page includes a cursor for the last returned row.
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

// MarshalQueryCursor encodes an event_time and seq position as one opaque
// string.
//
// [UnmarshalQueryCursor] decodes this shared format for paging callers. The
// encoding uses a UTC RFC3339Nano time and a decimal sequence separated by a
// vertical bar, then applies unpadded URL-safe base64.
//
//	cursor := MarshalQueryCursor(eventTime, sequence)
//	decodedTime, decodedSequence, err := UnmarshalQueryCursor(ctx, cursor)
func MarshalQueryCursor(eventTime time.Time, seq int64) string {
	raw := eventTime.UTC().Format(time.RFC3339Nano) + queryCursorSeparator + strconv.FormatInt(seq, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// UnmarshalQueryCursor decodes a position from [MarshalQueryCursor].
//
// The cursor is operator input. Each rejection identifies the failed component
// and logs the reason at the decoder. The log does not include the raw cursor.
//
//   - Invalid base64 reports an encoding error.
//   - A missing separator reports a shape error.
//   - Invalid time or sequence text reports the corresponding parse error.
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
