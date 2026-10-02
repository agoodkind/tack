package integration

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/audit"
)

//go:embed search_query_audit.sql
var searchAuditInvocationQuery string

// requireSearchAuditInvocation verifies one successful public request against
// its persisted SQL invocation.
func requireSearchAuditInvocation(parent context.Context, harness *MCPHarness, requestID string) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, harness.ledgerDSN)
	if err != nil {
		return fmt.Errorf("open search audit ledger: %w", err)
	}
	defer pool.Close()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var total, successful int64
		err := pool.QueryRow(ctx, searchAuditInvocationQuery, harness.orgID, audit.VerbMCPToolInvoked, requestID, audit.OutcomeOK).Scan(&total, &successful)
		if err != nil {
			return fmt.Errorf("query search audit invocation %s: %w", requestID, err)
		}
		if total == 1 && successful == 1 {
			return nil
		}
		if total > 1 || total != successful {
			return fmt.Errorf("search audit invocation %s has %d rows and %d successful rows, want one", requestID, total, successful)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for search audit invocation %s: %w", requestID, ctx.Err())
		case <-ticker.C:
		}
	}
}
