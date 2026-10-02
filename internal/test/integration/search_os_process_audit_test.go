package integration

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/audit"
)

//go:embed search_os_process_audit.sql
var actualProcessAuditQuery string

func requireActualProcessAudit(parent context.Context, harness *MCPHarness, requestID, tool string) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, harness.ledgerDSN)
	if err != nil {
		return fmt.Errorf("open actual-process audit ledger: %w", err)
	}
	defer pool.Close()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var total, successful int64
		err := pool.QueryRow(ctx, actualProcessAuditQuery, harness.orgID, audit.VerbMCPToolInvoked, requestID, audit.OutcomeOK, tool).Scan(&total, &successful)
		if err != nil {
			return fmt.Errorf("query actual-process audit invocation %s tool %s: %w", requestID, tool, err)
		}
		if total == 1 && successful == 1 {
			return nil
		}
		if total > 1 || total != successful {
			return fmt.Errorf("actual-process audit invocation %s tool %s has %d rows and %d successful rows, want one", requestID, tool, total, successful)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for actual-process audit invocation %s tool %s: %w", requestID, tool, ctx.Err())
		case <-ticker.C:
		}
	}
}
