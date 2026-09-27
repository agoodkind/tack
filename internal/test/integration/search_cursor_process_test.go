package integration

import (
	"slices"
	"testing"
)

// TestSearchCursorAcrossProcesses opens a session on one Tack process and
// alternates every continuation between two processes over the same
// FoundationDB and OpenSearch. A newly built third process then replays a
// committed continuation from durable state alone.
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
