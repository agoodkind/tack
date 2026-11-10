package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/audit"
)

func TestMCPMutationsPersistAndRecordChangesDuringSearchOutage(t *testing.T) {
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

	assertChangeEvents(t, harness, []string{
		"tack_create_issue",
		"tack_update_issue",
		"tack_delete_issue",
	})
}

// assertChangeEvents requires the FoundationDB outbox to contain at least one
// completed change event of each tool in the harness organization.
func assertChangeEvents(t *testing.T, harness *MCPHarness, toolNames []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for _, toolName := range toolNames {
		events, err := harnessOutboxMatching(ctx, harness, "tool "+toolName, func(event audit.Event) bool {
			return event.Context.Tool == toolName && event.Context.OrgID == harness.orgID &&
				event.Context.Phase == audit.PhaseChange && event.Outcome == audit.OutcomeCompleted
		})
		if err != nil {
			t.Fatalf("read the change events of %s: %v", toolName, err)
		}
		if len(events) == 0 {
			t.Fatalf("the FoundationDB outbox does not contain a completed change event of %s", toolName)
		}
	}
}
