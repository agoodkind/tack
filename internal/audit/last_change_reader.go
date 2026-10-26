package audit

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

//go:embed history_sql/*.sql
var historyStatements embed.FS

const (
	// LastChangeRowLimit caps the ledger rows that one ReadNodeHistory call
	// reads.
	LastChangeRowLimit = 5000
	// nodeHistoryPageSize is the row count of one page query.
	nodeHistoryPageSize = 500
	// nodeHistoryPageStatement is the page query in historyStatements.
	nodeHistoryPageStatement = "history_sql/node_history_page.sql"
)

// NodeHistoryFilter selects the ledger rows of one node in one org: the rows
// of the node itself and the relationship rows with the node as source or
// target. Oldest is inclusive and Latest is exclusive.
type NodeHistoryFilter struct {
	OrgID  uuid.UUID
	NodeID uuid.UUID
	Oldest time.Time
	Latest time.Time
	// RowLimit lowers the row cap below LastChangeRowLimit for tests that
	// need a truncated read without thousands of rows. Zero, a negative
	// value, or a value above LastChangeRowLimit uses LastChangeRowLimit.
	RowLimit int `exhaustruct:"optional"`
}

// rowLimit returns the row cap of one read.
func (filter NodeHistoryFilter) rowLimit() int {
	if filter.RowLimit <= 0 || filter.RowLimit > LastChangeRowLimit {
		return LastChangeRowLimit
	}
	return filter.RowLimit
}

// NodeHistoryRead reports one ReadNodeHistory call. Truncated is true when
// the call stopped at its row limit before the oldest row of the window.
// NextCursor then resumes the read after the last row read.
type NodeHistoryRead struct {
	RowsRead   int
	Truncated  bool
	NextCursor string
}

// historyPosition is the (event_time, seq, event_id) key that every row of
// the next page precedes.
type historyPosition struct {
	eventTime time.Time
	seq       int64
	eventID   uuid.UUID
}

// ReadNodeHistory reads the rows that filter selects, newest first, and calls
// visit for each row. It reads at most the row limit of filter. An empty
// cursor starts at Latest; a cursor from an earlier NextCursor starts after
// the last row that the earlier call read.
func (r *Reader) ReadNodeHistory(ctx context.Context, filter NodeHistoryFilter, cursor string, visit func(Row) error) (NodeHistoryRead, error) {
	result := NodeHistoryRead{RowsRead: 0, Truncated: false, NextCursor: ""}
	if err := r.checkNodeHistoryFilter(filter); err != nil {
		return result, err
	}
	position := historyPosition{eventTime: filter.Latest, seq: 0, eventID: uuid.Nil}
	if cursor != "" {
		resumed, err := unmarshalHistoryCursor(ctx, cursor)
		if err != nil {
			return result, err
		}
		position = resumed
	}
	rowLimit := filter.rowLimit()
	for {
		limit := min(nodeHistoryPageSize, rowLimit-result.RowsRead+1)
		rows, err := r.nodeHistoryPage(ctx, filter, position, limit)
		if err != nil {
			return result, err
		}
		for _, row := range rows {
			if result.RowsRead == rowLimit {
				result.Truncated = true
				result.NextCursor = marshalHistoryCursor(position)
				return result, nil
			}
			if err := visit(row); err != nil {
				slog.ErrorContext(ctx, "audit.node_history.visit_failed", slog.String("event_id", row.EventID.String()), slog.String("err", err.Error()))
				return result, fmt.Errorf("read the history of node %s: row %s: %w", filter.NodeID, row.EventID, err)
			}
			result.RowsRead++
			position = historyPosition{eventTime: row.EventTime, seq: row.Seq, eventID: row.EventID}
		}
		if len(rows) < limit {
			return result, nil
		}
	}
}

func (r *Reader) checkNodeHistoryFilter(filter NodeHistoryFilter) error {
	if r == nil || r.pool == nil {
		return errors.New("audit reader not configured")
	}
	if filter.OrgID == uuid.Nil || filter.NodeID == uuid.Nil {
		return errors.New("audit node history: org_id and node_id required")
	}
	if filter.Oldest.IsZero() || !filter.Latest.After(filter.Oldest) {
		return errors.New("audit node history: latest must be after oldest")
	}
	return nil
}

// nodeHistoryPage reads at most limit rows that precede position.
func (r *Reader) nodeHistoryPage(ctx context.Context, filter NodeHistoryFilter, position historyPosition, limit int) ([]Row, error) {
	statement, err := historyStatements.ReadFile(nodeHistoryPageStatement)
	if err != nil {
		slog.ErrorContext(ctx, "audit.node_history.statement_failed", slog.String("statement", nodeHistoryPageStatement), slog.String("err", err.Error()))
		return nil, fmt.Errorf("read %s: %w", nodeHistoryPageStatement, err)
	}
	rows, err := r.pool.Query(ctx, string(statement),
		filter.OrgID, filter.NodeID, filter.NodeID.String(), filter.Oldest,
		position.eventTime, position.seq, position.eventID, limit)
	if err != nil {
		slog.ErrorContext(ctx, "audit.node_history.query_failed", slog.String("node_id", filter.NodeID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("query the history of node %s: %w", filter.NodeID, err)
	}
	defer rows.Close()
	page := make([]Row, 0, limit)
	for rows.Next() {
		row, err := readAuditRow(rows)
		if err != nil {
			return nil, err
		}
		page = append(page, row)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.node_history.rows_failed", slog.String("node_id", filter.NodeID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("read the history rows of node %s: %w", filter.NodeID, err)
	}
	return page, nil
}
