package telemetry

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
)

// RequestPath counts ledger reads, ledger writes, and synchronous audit
// produces during one HTTP request. These counts measure the request-path
// criterion in TACK-507. Requests using only the product store record zeros.
type RequestPath struct {
	ledgerReads  atomic.Int64
	ledgerWrites atomic.Int64
	syncProduces atomic.Int64
}

// RequestPathCounts contains one request's totals after the handler returns.
type RequestPathCounts struct {
	LedgerReads  int64
	LedgerWrites int64
	SyncProduces int64
}

type requestPathKey struct{}

// WithRequestPath attaches fresh counters to ctx and returns them.
// Callers can read the totals after the request handler returns.
func WithRequestPath(ctx context.Context) (context.Context, *RequestPath) {
	counts := new(RequestPath)
	return context.WithValue(ctx, requestPathKey{}, counts), counts
}

// RequestPathFrom returns the counters attached to ctx. The second result
// reports whether the context includes counters.
func RequestPathFrom(ctx context.Context) (*RequestPath, bool) {
	counts, ok := ctx.Value(requestPathKey{}).(*RequestPath)
	return counts, ok
}

// Counts reads the totals.
func (p *RequestPath) Counts() RequestPathCounts {
	return RequestPathCounts{
		LedgerReads:  p.ledgerReads.Load(),
		LedgerWrites: p.ledgerWrites.Load(),
		SyncProduces: p.syncProduces.Load(),
	}
}

// CountLedgerQuery counts one ledger query for the request in ctx and
// classifies the statement. It does not count queries without request counters.
func CountLedgerQuery(ctx context.Context, sql string) {
	counts, ok := RequestPathFrom(ctx)
	if !ok {
		return
	}

	if ledgerQueryReads(sql) {
		counts.ledgerReads.Add(1)
		return
	}

	counts.ledgerWrites.Add(1)
}

// CountSyncProduce counts one synchronous broker produce for the request
// in ctx. It does not count produces without request counters.
func CountSyncProduce(ctx context.Context) {
	counts, ok := RequestPathFrom(ctx)
	if !ok {
		return
	}

	counts.syncProduces.Add(1)
}

// ledgerQueryReads classifies SELECT statements as reads. It also classifies
// WITH statements as reads unless a clause inserts, updates, or deletes rows.
// It classifies other statements as writes.
func ledgerQueryReads(sql string) bool {
	upper := strings.ToUpper(strings.TrimSpace(sql))
	switch {
	case strings.HasPrefix(upper, "SELECT"):
		return true
	case strings.HasPrefix(upper, "WITH"):
		return !strings.Contains(upper, "INSERT ") &&
			!strings.Contains(upper, "UPDATE ") &&
			!strings.Contains(upper, "DELETE ")
	default:
		return false
	}
}

// RequestPathCounter attaches counters to each HTTP request and logs the totals
// after the handler returns. The outer request logger supplies the request ID.
func RequestPathCounter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, counts := WithRequestPath(r.Context())
		next.ServeHTTP(w, r.WithContext(ctx))
		totals := counts.Counts()
		L(ctx).InfoContext(
			ctx,
			"request.path_counted",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int64("ledger_reads", totals.LedgerReads),
			slog.Int64("ledger_writes", totals.LedgerWrites),
			slog.Int64("sync_produces", totals.SyncProduces),
		)
	})
}
