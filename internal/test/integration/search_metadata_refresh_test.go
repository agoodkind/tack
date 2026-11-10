package integration

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
)

// TestSearchMetadataAfterStartup verifies that tack_list_property_defs lists
// a definition declared after process startup. The node stores the property
// value before the declaration, and indexed pages initially omit it. The
// running process must then find the node by that value without a restart or
// index change. Pages for a node without the property must retain identical
// semantic field bytes.
func TestSearchMetadataAfterStartup(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	entry := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	unaffected := putOpaqueNode(t, fixture, kind, entry, "unaffected node", "copper meadow lantern", readerExcludedValue)
	drainSearchWork(t, fixture.Worker, 2000)
	before := pagesOf(t, fixture, []uuid.UUID{unaffected})

	name := opaqueSearchKey("p")
	affected := putUndeclaredNode(t, fixture, kind, entry, name, "zephyr orchard beacon")
	drainSearchWork(t, fixture.Worker, 2000)
	undeclared := searchNodePages(t, fixture.Client, fixture.Index, affected, false)
	if len(undeclared) == 0 {
		t.Fatal("a node with an undeclared property value has no indexed pages")
	}
	for _, page := range undeclared {
		if page.PageText != nil && strings.Contains(*page.PageText, "zephyr orchard beacon") {
			t.Fatalf("page %s of the node indexes the undeclared property value", page.ID)
		}
	}

	definition := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: workspace.OrgID, Name: name, Type: node.PropertyType(opaqueSearchKey("t")),
		Search: &node.SearchProjection{Include: true, Order: 2, Rule: node.TextRule{Mode: node.TextRuleScalar}},
	}
	if err := fixture.Stores.PropertyDefs.Set(WithTestOperation(t, t.Context()), definition); err != nil {
		t.Fatalf("store property definition after startup: %v", err)
	}
	listing := fixture.Harness.Call(t, "tack_list_property_defs", datagen.ToolArguments{WorkspaceReference: fixture.Harness.Workspace})
	if !strings.Contains(listing.Text(), name) {
		t.Fatalf("the running MCP server does not list definition %s:\n%s", name, listing.Text())
	}
	drainSearchWork(t, fixture.Worker, 2000)

	requireAliasTarget(t, fixture)
	if pages := searchNodePages(t, fixture.Client, fixture.Index, affected, false); len(pages) == 0 {
		t.Fatal("the affected node has no pages after its property was declared")
	}
	results := callEverySearchPage(t, "zephyr orchard beacon", fixture.Harness)
	if !slices.Contains(results.IDs, affected) {
		t.Fatalf("search for the newly declared value returned %v, want node %s", results.IDs, affected)
	}
	requireSemanticPreserved(t, before, pagesOf(t, fixture, []uuid.UUID{unaffected}))
}
