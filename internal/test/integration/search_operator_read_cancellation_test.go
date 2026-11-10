package integration_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/testenv"
)

type readAdmissionQuery struct{}

// admissionCancellation observes the real driver's successful admission query.
type admissionCancellation struct {
	cancel context.CancelFunc
}

func (observer admissionCancellation) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, readAdmissionQuery{}, strings.Contains(data.SQL, "public.ops_read_begin"))
}

func (observer admissionCancellation) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	admission, _ := ctx.Value(readAdmissionQuery{}).(bool)
	if admission && data.Err == nil {
		observer.cancel()
	}
}

func cancellationReadPool(t *testing.T, cancel context.CancelFunc) *pgxpool.Pool {
	t.Helper()
	settings, err := pgxpool.ParseConfig(testenv.Ledger(t))
	if err != nil {
		t.Fatalf("parse the test ledger address: %v", err)
	}
	settings.ConnConfig.Tracer = admissionCancellation{cancel: cancel}
	pool, err := pgxpool.NewWithConfig(t.Context(), settings)
	if err != nil {
		t.Fatalf("open the observed read pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
