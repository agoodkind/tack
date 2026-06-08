package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

// defaultRecentWindow is the fallback boundary between the ClickHouse and
// Yugabyte read tiers when none is configured.
const defaultRecentWindow = 30 * 24 * time.Hour

// RowQuerier is the read surface the MCP audit-query handler depends on. Both
// the Yugabyte Reader and the QueryRouter satisfy it.
type RowQuerier interface {
	Query(ctx context.Context, f QueryFilter) ([]Row, error)
}

// QueryRouter sends recent-window audit queries to ClickHouse and everything
// else to Yugabyte. ClickHouse is the fast analytical tier; Yugabyte is the
// canonical store that always holds the full history and the hash chain.
type QueryRouter struct {
	yb           RowQuerier
	ch           RowQuerier
	recentWindow time.Duration
}

// NewQueryRouter builds a router over the canonical Yugabyte reader and an
// optional ClickHouse reader. A nil ClickHouse reader sends every query to
// Yugabyte. A recentWindow of zero or less falls back to 30 days.
func NewQueryRouter(yb *Reader, ch *ClickHouseReader, recentWindow time.Duration) *QueryRouter {
	router := &QueryRouter{yb: yb, ch: nil, recentWindow: recentWindow}
	if ch != nil {
		router.ch = ch
	}
	if router.recentWindow <= 0 {
		router.recentWindow = defaultRecentWindow
	}
	return router
}

// Query routes by the requested window. When ClickHouse is configured and the
// whole range starts within the recent window, it reads the OLAP projection;
// otherwise it reads canonical Yugabyte.
func (r *QueryRouter) Query(ctx context.Context, f QueryFilter) ([]Row, error) {
	cutoff := clock.Now().Add(-r.recentWindow)
	if r.ch != nil && !f.Oldest.Before(cutoff) {
		telemetry.L(ctx).Debug("audit.query.route", slog.String("tier", "clickhouse"))
		rows, err := r.ch.Query(ctx, f)
		if err == nil {
			return rows, nil
		}
		// ClickHouse is best effort. On any read error fall back to the canonical
		// Yugabyte store so a recent-window query is never silently incomplete
		// during a ClickHouse outage; the OLAP tier reconverges via the backfill
		// job (TACK-316).
		slog.WarnContext(ctx, "audit.query.clickhouse_fallback_yugabyte", slog.String("err", err.Error()))
	}
	telemetry.L(ctx).Debug("audit.query.route", slog.String("tier", "yugabyte"))
	rows, err := r.yb.Query(ctx, f)
	if err != nil {
		slog.ErrorContext(ctx, "audit.query.route_failed", slog.String("tier", "yugabyte"), slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit query route yugabyte: %w", err)
	}
	return rows, nil
}
