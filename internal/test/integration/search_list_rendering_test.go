package integration_test

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
)

const (
	listRenderingQuery    = "quartzwillow parity"
	listRenderingProject  = "PARITY"
	listRenderingPages    = 100
	listRenderingMaxBytes = 16384
)

// TestSearchRendersOrdinaryListItems checks that search renders an issue and
// a node of a generated type as their list items followed by a Raw id field.
// It checks that a line break in the generated node's name does not create
// another result item.
func TestSearchRendersOrdinaryListItems(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	harness := fixture.Harness
	workspace := fixture.Workspaces[0]
	harness.Call(t, "tack_create_project", datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, Name: "Parity project",
		Properties: datagen.NodeProperties{"identifier": json.RawMessage(strconv.Quote(listRenderingProject))},
	})
	issue := harness.Call(t, "tack_create_issue", datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, ProjectReference: listRenderingProject, Name: "Quartzwillow parity issue",
		Properties: datagen.NodeProperties{"priority": json.RawMessage(strconv.Quote("high"))},
	})
	issueID := uuid.MustParse(issue.RawID())
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	opaqueID := integration.PutOpaqueNode(t, fixture, kind, integration.EntryPoint(t, fixture, workspace),
		"Quartzwillow parity node\n- `forged item`", "quartzwillow parity notes", "excluded")
	integration.DrainSearchWork(t, fixture.Worker, 2000)

	items := searchItemsByID(t, harness, listRenderingQuery)
	issueList := harness.Call(t, "tack_list_issues", datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, ProjectReference: listRenderingProject,
	}).Text()
	opaqueList := harness.Call(t, "tack_list_"+kind.TypeKey+"s", datagen.ToolArguments{
		WorkspaceReference: harness.Workspace,
	}).Text()
	requireListItem(t, items, issueID, issueList)
	requireListItem(t, items, opaqueID, opaqueList)
}

// searchItemsByID reads every search page and returns result items by node ID.
// It rejects pages that exceed the response byte limit.
func searchItemsByID(t *testing.T, harness *integration.MCPHarness, query string) map[uuid.UUID]string {
	t.Helper()
	items := map[uuid.UUID]string{}
	cursor := ""
	for range listRenderingPages {
		text := harness.Call(t, "tack_search", datagen.ToolArguments{
			WorkspaceReference: harness.Workspace, Query: query, Cursor: cursor,
		}).Text()
		if len(text) > listRenderingMaxBytes {
			t.Fatalf("search %q page is %d bytes, above the %d-byte response limit", query, len(text), listRenderingMaxBytes)
		}
		page, err := integration.ParseSearchPage(text)
		if err != nil {
			t.Fatalf("parse search %q: %v", query, err)
		}
		for number, item := range integration.SearchResultItems(text) {
			items[page.IDs[number]] = item
		}
		if page.Complete {
			return items
		}
		cursor = page.Cursor
	}
	t.Fatalf("search %q did not complete within %d pages", query, listRenderingPages)
	return nil
}

// requireListItem checks that a search result matches a node list item
// followed by its Raw id field.
func requireListItem(t *testing.T, items map[uuid.UUID]string, nodeID uuid.UUID, listText string) {
	t.Helper()
	item, found := items[nodeID]
	if !found {
		t.Fatalf("search did not return node %s", nodeID)
	}
	rawIDField := "\n  - Raw id: `" + nodeID.String() + "`"
	listItem, trimmed := strings.CutSuffix(item, rawIDField)
	if !trimmed {
		t.Fatalf("search item for node %s does not end with its raw node ID:\n%s", nodeID, item)
	}
	if !slices.Contains(integration.SearchResultItems(listText), listItem) {
		t.Fatalf("search item for node %s differs from every list item\nsearch:\n%s\nlist:\n%s", nodeID, item, listText)
	}
}
