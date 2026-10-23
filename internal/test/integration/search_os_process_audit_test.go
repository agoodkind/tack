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

// requireActualProcessAudit waits for one completed read event in the FoundationDB outbox for a read tool.
// For a tool that changes state, it waits for one successful mcp.tool_invoked row in the ledger.
func requireActualProcessAudit(parent context.Context, harness *MCPHarness, requestID, tool string) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if verb, covered := audit.ToolVerb(tool); covered && audit.IsRead(verb) {
		return requireActualProcessRead(ctx, harness, requestID, tool)
	}
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

// requireActualProcessRead waits until the FoundationDB outbox contains exactly
// one completed read event for requestID and tool.
func requireActualProcessRead(ctx context.Context, harness *MCPHarness, requestID, tool string) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := harnessReadEvents(ctx, harness, requestID)
		if err != nil {
			return err
		}
		if len(events) > 1 {
			return fmt.Errorf("expected one read event for request %s and tool %s, got %d", requestID, tool, len(events))
		}
		if len(events) == 1 {
			if events[0].Context.Tool != tool || events[0].Outcome != audit.OutcomeCompleted {
				return fmt.Errorf("read request %s has tool %s and outcome %s; expected tool %s and outcome %s",
					requestID, events[0].Context.Tool, events[0].Outcome, tool, audit.OutcomeCompleted)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for the read event for request %s and tool %s: %w", requestID, tool, ctx.Err())
		case <-ticker.C:
		}
	}
}
