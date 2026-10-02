package integration

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
)

const (
	// cursorIsolationQuery matches every node of the cursor isolation corpus.
	cursorIsolationQuery = "cobalt ledger"
	// cursorMismatchText is the tack_search refusal for a cursor that does not
	// match the request.
	cursorMismatchText = "the search cursor does not match this request or is no longer current"
)

// actorHarness returns a harness that calls tack_search as actor index of
// workspace, through the fixture's production handler.
func actorHarness(fixture queryFixture, workspace datagen.WorkspaceIdentity, index int) *MCPHarness {
	return &MCPHarness{
		driver: fixture.Harness.driver, handler: fixture.Harness.handler, token: workspace.Actors[index].Token,
		ledgerDSN: fixture.Harness.ledgerDSN, orgID: workspace.OrgID, Workspace: workspace.Slug, Project: "",
	}
}

// TestSearchCursorRejectsForeignCallerAndChangedType opens a session as one
// caller and continues it with that caller's cursor in three ways: as another
// member of the same organization, as a member of another organization in
// that member's own workspace, and as the same caller with a node type the
// session did not use. Each call must return the cursor mismatch refusal.
// The original caller then continues the session to a page of new nodes.
func TestSearchCursorRejectsForeignCallerAndChangedType(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	caller, foreign := fixture.Workspaces[0], otherOrganization(t, fixture)
	callerEntry := entryPoint(t, fixture, caller)
	kind := putOpaqueKind(t, fixture, caller.OrgID)
	for number := range 30 {
		putOpaqueNode(t, fixture, kind, callerEntry, fmt.Sprintf("Cobalt ledger %d", number), "cobalt ledger entry", "excluded")
	}
	drainSearchWork(t, fixture.Worker, 2000)
	first := callSearch(t, fixture.Harness, cursorIsolationQuery, "")
	if first.Complete || first.Cursor == "" {
		t.Fatalf("first page returned %d results and complete=%t, want a cursor", len(first.IDs), first.Complete)
	}

	sameOrganization := actorHarness(fixture, caller, 1)
	otherOrganizationCaller := actorHarness(fixture, foreign, 0)
	changedType := searchArguments(fixture.Harness, cursorIsolationQuery, first.Cursor)
	changedType["node_type"] = entryTypeKeys(t, fixture, caller.OrgID)[0]
	attempts := []struct {
		name      string
		harness   *MCPHarness
		arguments map[string]any
	}{
		{"another member of the organization", sameOrganization, searchArguments(sameOrganization, cursorIsolationQuery, first.Cursor)},
		{"a member of another organization", otherOrganizationCaller, searchArguments(otherOrganizationCaller, cursorIsolationQuery, first.Cursor)},
		{"the same caller with a changed node type", fixture.Harness, changedType},
	}
	for _, attempt := range attempts {
		text, isError, err := rawSearchCall(attempt.harness, attempt.arguments)
		if err != nil {
			t.Fatalf("%s: %v", attempt.name, err)
		}
		if !isError || !strings.Contains(text, cursorMismatchText) {
			t.Fatalf("%s continued another session with error=%t:\n%s", attempt.name, isError, text)
		}
	}

	second := callSearch(t, fixture.Harness, cursorIsolationQuery, first.Cursor)
	if len(second.IDs) == 0 {
		t.Fatal("the original caller's second page returned no node after the refused calls")
	}
	for _, id := range second.IDs {
		if slices.Contains(first.IDs, id) {
			t.Fatalf("the second page repeated node %s from the first page", id)
		}
	}
}
