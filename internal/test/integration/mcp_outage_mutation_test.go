package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/audit"
)

func TestMCPMutationsPersistAndRecordInvocationsDuringSearchOutage(t *testing.T) {
	harness := NewAuditedMCPHarness(t)
	createdName := "Outage ledger issue"
	created := callToolSuccess(t, harness, "tack_create_issue", map[string]any{
		"workspace_reference": harness.Workspace,
		"project_reference":   harness.Project,
		"name":                createdName,
	})
	issueID := rawNodeID(t, created)

	got := callToolSuccess(t, harness, "tack_get_issue", nodeIDArgs(harness, issueID))
	if !strings.Contains(got, createdName) {
		t.Fatalf("created issue is absent from FoundationDB-backed get:\n%s", got)
	}

	updatedName := "Outage ledger issue updated"
	callToolSuccess(t, harness, "tack_update_issue", map[string]any{
		"workspace_reference": harness.Workspace,
		"node_id":             issueID,
		"name":                updatedName,
	})
	got = callToolSuccess(t, harness, "tack_get_issue", nodeIDArgs(harness, issueID))
	if !strings.Contains(got, updatedName) {
		t.Fatalf("updated issue is absent from FoundationDB-backed get:\n%s", got)
	}

	callToolSuccess(t, harness, "tack_delete_issue", nodeIDArgs(harness, issueID))
	deleted := callToolRaw(t, harness, "tack_get_issue", nodeIDArgs(harness, issueID))
	if !deleted.IsError {
		t.Fatal("deleted issue remains readable from the FoundationDB-backed MCP boundary")
	}

	assertInvocationRows(t, harness, []string{
		"tack_create_issue",
		"tack_update_issue",
		"tack_delete_issue",
	})
}

func assertInvocationRows(t *testing.T, harness *MCPHarness, toolNames []string) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), harness.ledgerDSN)
	if err != nil {
		t.Fatalf("open audit ledger: %v", err)
	}
	t.Cleanup(pool.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	counts := make(map[string]int64, len(toolNames))
	for {
		complete := true
		for _, toolName := range toolNames {
			var count int64
			err := pool.QueryRow(ctx, `
				SELECT count(*)
				  FROM audit.events
				 WHERE org_id = $1
				   AND action = $2
				   AND context->>'tool' = $3
			`, harness.orgID, audit.VerbMCPToolInvoked, toolName).Scan(&count)
			if err != nil {
				t.Fatalf("query audit rows for %s: %v", toolName, err)
			}
			counts[toolName] = count
			if count != 1 {
				complete = false
			}
		}
		if complete {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("audit invocation rows did not appear: %v", counts)
		case <-ticker.C:
		}
	}
}
