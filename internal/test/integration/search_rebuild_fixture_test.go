package integration

import (
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// rebuildDeadline bounds one complete index replacement in a test. Each
// claim pause waits one worker lease.
const rebuildDeadline = 10 * time.Minute

// beginRebuild begins one replacement through the production store and
// deletes its target when the test ends.
func beginRebuild(t *testing.T, fixture queryFixture, request searchdomain.BeginRebuild) searchdomain.Rebuild {
	t.Helper()
	rebuild, err := fixture.Stores.SearchRebuilds(clock.Wall{}).BeginRebuild(WithTestOperation(t, t.Context()), request)
	if err != nil {
		t.Fatalf("begin index replacement: %v", err)
	}
	t.Cleanup(func() { deleteNativeIndex(t, fixture.Client, rebuild.TargetIndex) })
	return rebuild
}

// runRebuildUntil runs worker slices until done accepts the replacement
// state. A missing replacement passes found false.
func runRebuildUntil(t *testing.T, fixture queryFixture, done func(searchdomain.Rebuild, bool) bool) searchdomain.Rebuild {
	t.Helper()
	rebuilds := fixture.Stores.SearchRebuilds(clock.Wall{})
	deadline := time.Now().Add(rebuildDeadline)
	for time.Now().Before(deadline) {
		current, found, err := rebuilds.CurrentRebuild(WithTestOperation(t, t.Context()))
		if err != nil {
			t.Fatalf("read index replacement: %v", err)
		}
		if done(current, found) {
			return current
		}
		runSliceOrPause(t, fixture.Worker)
	}
	t.Fatalf("index replacement stayed short of the expected state for %s", rebuildDeadline)
	return searchdomain.Rebuild{}
}

// rebuildFinished accepts the state after the replacement record is gone.
func rebuildFinished(_ searchdomain.Rebuild, found bool) bool { return !found }

// shortenSearchSessions sets a 3 second idle limit and a 6 second absolute
// limit for search sessions. Retirement of the old index then waits at most
// 6 seconds for a session that a test search opened. A test must set the
// limits before the fixture builds the graph.
func shortenSearchSessions(t *testing.T) {
	t.Helper()
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", "3s")
	t.Setenv("OPENSEARCH_SESSION_ABSOLUTE_TIMEOUT", "6s")
}

// inState accepts a replacement in state.
func inState(state searchdomain.RebuildState) func(searchdomain.Rebuild, bool) bool {
	return func(current searchdomain.Rebuild, found bool) bool { return found && current.State == state }
}

// pagesCurrent reports whether index stores one active document of the
// current revision for every page the production reader returns.
func pagesCurrent(t *testing.T, fixture queryFixture, index string, nodeID uuid.UUID) bool {
	t.Helper()
	pages := readSearchPages(t, fixture.Stores, nodeID, runtimePageBytes)
	documents := searchNodePages(t, fixture.Client, index, nodeID, false)
	matched := len(documents) == len(pages)
	for number := range min(len(documents), len(pages)) {
		matched = matched && documents[number].NodeRevision == pages[number].Revision
	}
	return matched
}

// requireSearchFinds requires public search for query to return every node
// and returns the complete results.
func requireSearchFinds(t *testing.T, fixture queryFixture, query string, nodes ...uuid.UUID) []uuid.UUID {
	t.Helper()
	results := callEverySearchPage(t, query, fixture.Harness)
	for _, nodeID := range nodes {
		if !slices.Contains(results.IDs, nodeID) {
			t.Fatalf("search %q returned %v, want node %s", query, results.IDs, nodeID)
		}
	}
	return results.IDs
}

// editOpaqueNode replaces the included value of one node through the
// production write path.
func editOpaqueNode(t *testing.T, fixture queryFixture, kind opaqueKind, nodeID uuid.UUID, included string) {
	t.Helper()
	current, err := fixture.Stores.Nodes.Get(WithTestOperation(t, t.Context()), kind.OrgID, nodeID)
	if err != nil {
		t.Fatalf("read node %s: %v", nodeID, err)
	}
	props := maps.Clone(current.Props)
	props[kind.IncludedKey] = mustJSON(included)
	current.Props = props
	current.UpdatedAt = clock.Now().UTC()
	view := &node.NodeView{
		ID: current.ID, OrgID: current.OrgID, NodeType: current.NodeType, Name: current.Name,
		Props: props, CreatedAt: current.CreatedAt, UpdatedAt: current.UpdatedAt,
	}
	if err := fixture.Stores.Nodes.Set(WithTestOperation(t, t.Context()), current, view); err != nil {
		t.Fatalf("edit node %s: %v", nodeID, err)
	}
}

// requireServing requires FoundationDB and the public alias to select index,
// and requires the previous index to be deleted.
func requireServing(t *testing.T, fixture queryFixture, index, previous string) {
	t.Helper()
	serving, err := fixture.Stores.ServingSearchIndex(WithTestOperation(t, t.Context()))
	if err != nil || serving != index {
		t.Fatalf("serving index = %q, error = %v, want %q", serving, err, index)
	}
	target, err := fixture.Adapter.AliasTarget(WithTestOperation(t, t.Context()), search.PublicAlias)
	if err != nil || target != index {
		t.Fatalf("public alias target = %q, error = %v, want %q", target, err, index)
	}
	if previous == "" {
		return
	}
	if _, err := fixture.Adapter.IndexSettings(WithTestOperation(t, t.Context()), previous); err == nil {
		t.Fatalf("previous index %s still exists", previous)
	}
}

// requireCurrentPages requires the active pages of each node in index to
// equal the pages the production reader returns now.
func requireCurrentPages(t *testing.T, fixture queryFixture, index string, nodes []uuid.UUID) {
	t.Helper()
	for _, nodeID := range nodes {
		pages := readSearchPages(t, fixture.Stores, nodeID, runtimePageBytes)
		requireIndexedPages(t, searchNodePages(t, fixture.Client, index, nodeID, false), pages, 1)
	}
}

// pagesIn returns the active pages of every node in index, in order.
func pagesIn(t *testing.T, fixture queryFixture, index string, nodes []uuid.UUID) []searchPageSource {
	t.Helper()
	pages := make([]searchPageSource, 0, len(nodes)*2)
	for _, nodeID := range nodes {
		pages = append(pages, searchNodePages(t, fixture.Client, index, nodeID, false)...)
	}
	return pages
}

// requireDeletedAbsent requires no active page of nodeID in index.
func requireDeletedAbsent(t *testing.T, fixture queryFixture, index string, nodeID uuid.UUID) {
	t.Helper()
	if pages := searchNodePages(t, fixture.Client, index, nodeID, false); len(pages) != 0 {
		t.Fatalf("deleted node %s has %d active pages in %s", nodeID, len(pages), index)
	}
}

// requireTopologyRejected requires a replacement to fail when its routing
// shard count is not a multiple of its primary shard count.
func requireTopologyRejected(t *testing.T, fixture queryFixture, request searchdomain.BeginRebuild) {
	t.Helper()
	request.PrimaryShards, request.RoutingShards = 3, 4
	if _, err := fixture.Stores.SearchRebuilds(clock.Wall{}).BeginRebuild(WithTestOperation(t, t.Context()), request); err == nil {
		t.Fatal("a replacement with 3 primary shards and 4 routing shards was accepted")
	}
}

// requireSecondRefused requires a second concurrent replacement to fail.
func requireSecondRefused(t *testing.T, fixture queryFixture, request searchdomain.BeginRebuild) {
	t.Helper()
	_, err := fixture.Stores.SearchRebuilds(clock.Wall{}).BeginRebuild(WithTestOperation(t, t.Context()), request)
	if !errors.Is(err, searchdomain.ErrRebuildInProgress) {
		t.Fatalf("second replacement error = %v, want %v", err, searchdomain.ErrRebuildInProgress)
	}
}
