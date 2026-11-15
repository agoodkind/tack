package integration_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRuntimeAuditReadRecordingFailure cancels the request before the outbox transaction.
func TestRuntimeAuditReadRecordingFailure(t *testing.T) {
	server := startPublicServer(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	handler := server.graph.Requests.Health(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("health handler ran after admission failed")
		}),
	)
	response := serveRecorded(ctx, handler, newRequestID(t, "admission-failure"))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("response status = %d, want 503", response.Code)
	}
}

// rolePool opens a pool that runs every statement as role.
func rolePool(t *testing.T, dsn, role string) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig failed for the ledger address: %v", err)
	}

	config.ConnConfig.RuntimeParams["role"] = role
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig failed for the %s pool: %v", role, err)
	}

	t.Cleanup(pool.Close)

	return pool
}
