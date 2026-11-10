package integration_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

const (
	listRenderingQuery      = "quartzwillow parity"
	listRenderingProject    = "PARITY"
	listRenderingPages      = 100
	listRenderingMaxBytes   = 16384
	listRenderingNodes      = 30
	listRenderingListLimit  = 100
	listRenderingLongName   = 3000
	listRenderingLongState  = 300
	listRenderingReference  = "- Reference: `"
	listRenderingShortening = "…"
)

// searchTraversal records the pages and result items of one complete search.
type searchTraversal struct {
	pages   []string
	cursors []string
	items   []string
}

// TestSearchRendersOrdinaryListItems compares search and list output for
// issues and a generated node type across multiple search pages.
func TestSearchRendersOrdinaryListItems(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	harness := fixture.Harness
	workspace := fixture.Workspaces[0]
	harness.Call(t, "tack_create_project", datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, Name: "Parity project",
		Properties: datagen.NodeProperties{"identifier": json.RawMessage(strconv.Quote(listRenderingProject))},
	})
	createParityIssue(t, harness, "Quartzwillow parity issue", datagen.NodeProperties{
		"priority": json.RawMessage(strconv.Quote("high")),
	})
	longState := "Parity " + strings.Repeat("s", listRenderingLongState)
	harness.Call(t, "tack_create_state", datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, ProjectReference: listRenderingProject, Name: longState,
	})
	longName := "Quartzwillow parity oversized " + strings.Repeat("x", listRenderingLongName)
	oversized := createParityIssue(t, harness, longName, datagen.NodeProperties{
		"priority": json.RawMessage(strconv.Quote("low")),
		"state":    json.RawMessage(strconv.Quote(listRenderingProject + "::" + longState)),
	})
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	allowListing(t, fixture, kind)
	entryID := integration.EntryPoint(t, fixture, workspace)
	for number := range listRenderingNodes {
		name := fmt.Sprintf("Quartzwillow parity node %02d", number)
		if number == 0 {
			name += "\n- `forged item`"
		}
		integration.PutOpaqueNode(t, fixture, kind, entryID, name, "quartzwillow parity notes", "excluded")
	}
	integration.DrainSearchWork(t, fixture.Worker, 2000)

	traversal := traverseParitySearch(t, harness)
	if len(traversal.cursors) == 0 {
		t.Fatalf("search returned %d items on one page, want more than one page", len(traversal.items))
	}
	requireCursorReplays(t, harness, traversal)
	issueList := callParityList(t, harness, "tack_list_issues", listRenderingProject)
	opaqueList := callParityList(t, harness, "tack_list_"+kind.TypeKey+"s", "")
	oversizedTitle := "- `" + oversized + "`"
	for _, listItem := range append(integration.SearchResultItems(issueList), integration.SearchResultItems(opaqueList)...) {
		if strings.HasPrefix(listItem, oversizedTitle+"\n") {
			requireShortenedItem(t, traversal.items, oversizedTitle, listItem)
			continue
		}
		if count := countItem(traversal.items, listItem); count != 1 {
			t.Fatalf("search returned the list item %d times, want once:\n%s", count, listItem)
		}
	}
}

// allowListing enables the list and read operations on the generated node type.
// The server registers the list tool for types with those operations.
func allowListing(t *testing.T, fixture integration.QueryFixture, kind integration.OpaqueKind) {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	types, err := fixture.Stores.NodeTypes.List(ctx, kind.OrgID)
	if err != nil {
		t.Fatalf("list node types of org %s: %v", kind.OrgID, err)
	}
	for _, stored := range types {
		if stored.TypeKey != kind.TypeKey {
			continue
		}
		stored.AllowedOps = []node.Op{node.OpList, node.OpRead}
		if err := fixture.Stores.NodeTypes.Set(ctx, stored); err != nil {
			t.Fatalf("store the operations of node type %s: %v", kind.TypeKey, err)
		}
		return
	}
	t.Fatalf("node type %s is missing", kind.TypeKey)
}

// createParityIssue creates an issue in the parity project and returns its
// printed reference.
func createParityIssue(t *testing.T, harness *integration.MCPHarness, name string, properties datagen.NodeProperties) string {
	t.Helper()
	text := harness.Call(t, "tack_create_issue", datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, ProjectReference: listRenderingProject, Name: name, Properties: properties,
	}).Text()
	_, after, found := strings.Cut(text, listRenderingReference)
	reference, _, closed := strings.Cut(after, "`")
	if !found || !closed || reference == "" {
		t.Fatalf("issue creation did not print a reference:\n%s", text)
	}
	return reference
}

// traverseParitySearch follows every cursor of the parity query. It requires
// each page to fit the response limit and each item to appear once.
func traverseParitySearch(t *testing.T, harness *integration.MCPHarness) searchTraversal {
	t.Helper()
	traversal := searchTraversal{pages: nil, cursors: nil, items: nil}
	cursor := ""
	for range listRenderingPages {
		text := callParitySearch(t, harness, cursor)
		traversal.pages = append(traversal.pages, text)
		for _, item := range integration.SearchResultItems(text) {
			if countItem(traversal.items, item) > 0 {
				t.Fatalf("search returned an item twice:\n%s", item)
			}
			traversal.items = append(traversal.items, item)
		}
		cursor = pageCursor(text)
		if cursor == "" {
			return traversal
		}
		traversal.cursors = append(traversal.cursors, cursor)
	}
	t.Fatalf("search %q did not complete within %d pages", listRenderingQuery, listRenderingPages)
	return traversal
}

// requireCursorReplays calls search again with each cursor and requires the
// same page text that the first traversal returned for that cursor.
func requireCursorReplays(t *testing.T, harness *integration.MCPHarness, traversal searchTraversal) {
	t.Helper()
	for number, cursor := range traversal.cursors {
		if replayed := callParitySearch(t, harness, cursor); replayed != traversal.pages[number+1] {
			t.Fatalf("replay of page %d differs\nfirst:\n%s\nreplay:\n%s", number+2, traversal.pages[number+1], replayed)
		}
	}
}

// requireShortenedItem requires exactly one search item with the list item's
// title. The search item must use fewer bytes and include an ellipsis.
func requireShortenedItem(t *testing.T, items []string, title, listItem string) {
	t.Helper()
	matches := 0
	for _, item := range items {
		if !strings.HasPrefix(item, title+"\n") {
			continue
		}
		matches++
		if len(item) >= len(listItem) || !strings.Contains(item, listRenderingShortening) {
			t.Fatalf("search item for %s is not shortened\nsearch:\n%s", title, item)
		}
	}
	if matches != 1 {
		t.Fatalf("search returned %d items titled %s, want 1", matches, title)
	}
}

func callParitySearch(t *testing.T, harness *integration.MCPHarness, cursor string) string {
	t.Helper()
	text := harness.Call(t, "tack_search", datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, Query: listRenderingQuery, Cursor: cursor,
	}).Text()
	if len(text) > listRenderingMaxBytes {
		t.Fatalf("search page is %d bytes, above the %d-byte response limit", len(text), listRenderingMaxBytes)
	}
	if !strings.Contains(text, fmt.Sprintf("\n%d results shown.\n", len(integration.SearchResultItems(text)))) {
		t.Fatalf("search page count line does not match its items:\n%s", text)
	}
	return text
}

func callParityList(t *testing.T, harness *integration.MCPHarness, tool, project string) string {
	t.Helper()
	text := harness.Call(t, tool, datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, ProjectReference: project, Limit: listRenderingListLimit,
	}).Text()
	if pageCursor(text) != "" {
		t.Fatalf("%s returned more than %d items", tool, listRenderingListLimit)
	}
	return text
}

func pageCursor(text string) string {
	_, after, found := strings.Cut(text, integration.SearchCursorMarker)
	if !found {
		return ""
	}
	cursor, _, _ := strings.Cut(after, "`")
	return cursor
}

func countItem(items []string, wanted string) int {
	count := 0
	for _, item := range items {
		if item == wanted {
			count++
		}
	}
	return count
}
