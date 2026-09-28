package integration

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
)

// TestSearchMetadataAfterStartup requires a running Tack process to list a
// property definition declared after startup in tack_list_property_defs. The
// process must then return a node for its value under that property without
// a restart or an index change. The node stores the value before the
// declaration exists. The pages of a node without that property must keep
// their semantic fields byte for byte.
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
	runSlicesIgnoringFailures(t, fixture.Worker, 200)
	if pages := searchNodePages(t, fixture.Client, fixture.Index, affected, false); len(pages) != 0 {
		t.Fatalf("a node with an undeclared property value has %d indexed pages", len(pages))
	}

	definition := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: workspace.OrgID, Name: name, Type: node.PropertyType(opaqueSearchKey("t")),
		Search: &node.SearchProjection{Include: true, Order: 2, Rule: node.TextRule{Mode: node.TextRuleScalar}},
	}
	if err := fixture.Stores.PropertyDefs.Set(t.Context(), definition); err != nil {
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
