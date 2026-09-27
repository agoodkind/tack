package integration

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestSearchUnavailableWhileDisabled indexes a node while the public flag is
// false. Every call, including exact references, returns the exact message.
func TestSearchUnavailableWhileDisabled(t *testing.T) {
	options := defaultQueryOptions()
	options.Public = false
	fixture := newQueryFixture(t, options)
	workspace := fixture.Workspaces[0]
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	nodeID := putOpaqueNode(t, fixture, kind, entryPoint(t, fixture, workspace), "Quartz ledger rotation", "quartz ledger rotation notes", "hidden")
	drainSearchWork(t, fixture.Worker, 500)
	for _, arguments := range []map[string]any{
		searchArguments(fixture.Harness, "quartz ledger", ""),
		searchArguments(fixture.Harness, "Quartz ledger rotation", ""),
		searchArguments(fixture.Harness, nodeID.String(), ""),
		searchArguments(fixture.Harness, "quartz", "not-a-cursor"),
		{},
	} {
		text, isError, err := rawSearchCall(fixture.Harness, arguments)
		if err != nil {
			t.Fatalf("call search: %v", err)
		}
		if !isError || text != unavailableSearchMessage {
			t.Fatalf("disabled search returned isError=%t text %q", isError, text)
		}
	}
}

// TestSearchEmptyQuery requires empty, blank, and oversized queries to fail
// through the authenticated handler before inference.
func TestSearchEmptyQuery(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	for _, query := range []string{"", "   ", strings.Repeat("q", 1025)} {
		text, isError, err := rawSearchCall(fixture.Harness, searchArguments(fixture.Harness, query, ""))
		if err != nil {
			t.Fatalf("call search: %v", err)
		}
		if !isError || !strings.Contains(text, "query") {
			t.Fatalf("query of %d bytes returned isError=%t text %q", len(query), isError, text)
		}
	}
}

// excludedAuthValue is the value of the excluded property in
// TestSearchAuthenticatedResults.
const excludedAuthValue = "zirconium"

// TestSearchAuthenticatedResults requires an authenticated caller to find an
// indexed node and an anonymous caller to be refused. No stored page text
// contains the excluded value.
func TestSearchAuthenticatedResults(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	entryID := entryPoint(t, fixture, workspace)
	target := putOpaqueNode(t, fixture, kind, entryID, "Heliotrope cache rebuild", "heliotrope cache rebuild steps", excludedAuthValue)
	other := putOpaqueNode(t, fixture, kind, entryID, "Unrelated", "unrelated text", excludedAuthValue+" "+excludedAuthValue)
	drainSearchWork(t, fixture.Worker, 500)
	pages := callEverySearchPage(t, "heliotrope cache", fixture.Harness)
	if !slices.Contains(pages.IDs, target) {
		t.Fatalf("search did not return the indexed node %s: %v", target, pages.IDs)
	}
	for _, nodeID := range []uuid.UUID{target, other} {
		requireStoredTextExcludes(t, fixture, nodeID, excludedAuthValue)
	}
	requireUnauthenticatedRefused(t, fixture.Harness)
}

// requireStoredTextExcludes reads the active page documents of nodeID from
// OpenSearch and requires that no page_text contains value.
func requireStoredTextExcludes(t *testing.T, fixture queryFixture, nodeID uuid.UUID, value string) {
	t.Helper()
	pages := searchNodePages(t, fixture.Client, fixture.Index, nodeID, false)
	if len(pages) == 0 {
		t.Fatalf("node %s has no active page documents", nodeID)
	}
	for _, page := range pages {
		if page.PageText == nil {
			t.Fatalf("page %s of node %s has no page_text", page.ID, nodeID)
		}
		if strings.Contains(*page.PageText, value) {
			t.Fatalf("page %s of node %s stores the excluded value %q", page.ID, nodeID, value)
		}
	}
}
