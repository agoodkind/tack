package telemetry

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
)

// RequestPath counts the round trips one HTTP request makes before it
// answers: ledger queries split into reads and writes, and audit events
// produced synchronously to the broker. The counts are the evidence for the
// request-path criterion of the backup rearchitecture (TACK-507): a request
// that answers from the product store alone shows zeros here.
type RequestPath struct {
	ledgerReads  atomic.Int64
	ledgerWrites atomic.Int64
	syncProduces atomic.Int64
}

// RequestPathCounts is one request's totals, read after the request answered.
type RequestPathCounts struct {
	LedgerReads  int64
	LedgerWrites int64
	SyncProduces int64
}

type requestPathKey struct{}

// WithRequestPath attaches a fresh counter set to ctx and returns it, so the
// caller that installs it can read the totals after the request answered.
func WithRequestPath(ctx context.Context) (context.Context, *RequestPath) {
	counts := new(RequestPath)
	return context.WithValue(ctx, requestPathKey{}, counts), counts
}

// RequestPathFrom returns the counter set attached to ctx. The second result
// is false outside an HTTP request, where nothing counts.
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

// CountLedgerQuery records one ledger query on the request attached to ctx,
// classified by its statement. A ctx with no request attached counts nothing.
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

// CountSyncProduce records one synchronous broker produce on the request
// attached to ctx. A ctx with no request attached counts nothing.
func CountSyncProduce(ctx context.Context) {
	counts, ok := RequestPathFrom(ctx)
	if !ok {
		return
	}
	counts.syncProduces.Add(1)
}

// ledgerQueryReads reports whether a statement only reads. A SELECT reads. A
// statement that opens with WITH reads unless one of its clauses modifies
// rows. Everything else writes.
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

// RequestPathCounter is HTTP middleware that attaches a counter set to every
// request and logs the totals once the request answered. It sits inside the
// request logger so the line carries the request id.
func RequestPathCounter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, counts := WithRequestPath(r.Context())
		next.ServeHTTP(w, r.WithContext(ctx))
		totals := counts.Counts()
		L(ctx).InfoContext(ctx, "request.path_counted",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int64("ledger_reads", totals.LedgerReads),
			slog.Int64("ledger_writes", totals.LedgerWrites),
			slog.Int64("sync_produces", totals.SyncProduces),
		)
	})
}
