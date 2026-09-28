package integration

import (
	"slices"
	"testing"
)

// TestSearchCursorAcrossProcesses requires a traversal that alternates every
// continuation between two Tack processes to return each corpus node once. A
// newly built third process must replay a committed continuation from
// durable state alone. All three processes use the same FoundationDB and
// OpenSearch.
func TestSearchCursorAcrossProcesses(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	nodes := putCursorCorpus(t, fixture, "copper meadow", 140)
	second := processHarness(fixture.Harness, buildQueryGraph(t, fixture.Config))
	pages := callEverySearchPage(t, "copper meadow", fixture.Harness, second)
	requireCorpusOnce(t, pages.IDs, nodes, entryPoint(t, fixture, fixture.Workspaces[0]))

	first := callSearch(t, fixture.Harness, "copper meadow", "")
	next := callSearch(t, second, "copper meadow", first.Cursor)
	third := processHarness(fixture.Harness, buildQueryGraph(t, fixture.Config))
	replayed := callSearch(t, third, "copper meadow", first.Cursor)
	if !slices.Equal(replayed.IDs, next.IDs) || replayed.Cursor != next.Cursor {
		t.Fatalf("a restarted process replayed %v, want %v", replayed.IDs, next.IDs)
	}
}
