package integration

import (
	"encoding/json"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	// pitMutationQuery matches every issue of the point-in-time corpus.
	pitMutationQuery = "quartz meridian"
	// pitMutationName is the name of every corpus issue. Equal scores order
	// the matches by ascending node ID, and an issue created later has a
	// larger UUIDv7.
	pitMutationName = "Quartz meridian node"
	// pitEditedName replaces the name of the edited issue.
	pitEditedName     = "Basalt cinder node"
	pitEditedQuery    = "basalt cinder"
	pitOriginalNodes  = 40
	pitCreatedNodes   = 10
	pitTraversalPages = 100
)

// TestSearchCursorPointInTimeIgnoresLaterChanges opens a search session,
// then creates, edits, and deletes issues through the MCP tools and indexes
// every change. Under the session point in time, the raw engine matches
// after the session sort position are the same before and after the changes
// (spec lines 126-127). The public continuation returns the distinct nodes
// of those raw matches that the first page did not return, minus the
// deleted issue, which the summary check withholds (spec lines 128-129). A
// new session returns the created issues and the edited name and omits the
// deleted issue.
func TestSearchCursorPointInTimeIgnoresLaterChanges(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	harness := fixture.Harness
	identifier := "PIT" + strconv.FormatInt(clock.Now().UnixNano()%1_000_000, 10)
	harness.Call(t, "tack_create_project", datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, Name: "Point in time project",
		Properties: datagen.NodeProperties{"identifier": json.RawMessage(strconv.Quote(identifier))},
	})
	harness.Project = identifier
	for range pitOriginalNodes {
		createPITIssue(t, harness)
	}
	drainSearchWork(t, fixture.Worker, 4000)

	first := callSearch(t, harness, pitMutationQuery, "")
	if first.Complete || first.Cursor == "" {
		t.Fatalf("first page returned %d nodes and complete=%t, want a continuation", len(first.IDs), first.Complete)
	}
	session := cursorSession(t, fixture, first.Cursor)
	ranker := fixture.Adapter.Ranker(search.RankerSettings{KeepAlive: 5 * time.Minute, TokenBytes: 64 << 10, BatchSize: 100})
	before := rawMatchesFrom(t, ranker, session)
	remaining := distinctNodes(before, first.IDs)
	if len(remaining) < 2 {
		t.Fatalf("raw matches after the first page name %d nodes, want at least two", len(remaining))
	}
	edited, deleted := remaining[0], remaining[1]

	created := make([]uuid.UUID, 0, pitCreatedNodes)
	for range pitCreatedNodes {
		created = append(created, createPITIssue(t, harness))
	}
	harness.Call(t, "tack_update_issue", datagen.ToolArguments{WorkspaceReference: harness.Workspace, NodeID: edited.String(), Name: pitEditedName})
	harness.Call(t, "tack_delete_issue", datagen.ToolArguments{WorkspaceReference: harness.Workspace, NodeID: deleted.String()})
	drainSearchWork(t, fixture.Worker, 4000)

	if after := rawMatchesFrom(t, ranker, session); !slices.Equal(after, before) {
		t.Fatalf("raw matches under the session point in time changed: before %v, after %v", before, after)
	}
	continued := continueSearch(t, harness, first)
	wanted := make([]uuid.UUID, 0, len(remaining))
	for _, id := range remaining {
		if id != deleted {
			wanted = append(wanted, id)
		}
	}
	requireSameNodes(t, "the public continuation", continued, wanted)

	fresh := callEverySearchPage(t, pitMutationQuery, harness).IDs
	for _, id := range created {
		if !slices.Contains(fresh, id) {
			t.Fatalf("a new session omitted created issue %s", id)
		}
	}
	if slices.Contains(fresh, deleted) {
		t.Fatalf("a new session returned deleted issue %s", deleted)
	}
	if !slices.Contains(callEverySearchPage(t, pitEditedQuery, harness).IDs, edited) {
		t.Fatalf("a new session for %q omitted edited issue %s", pitEditedQuery, edited)
	}
}

// createPITIssue creates one corpus issue through tack_create_issue.
func createPITIssue(t *testing.T, harness *MCPHarness) uuid.UUID {
	t.Helper()
	arguments := harness.projectArgs()
	arguments.Name = pitMutationName
	created := harness.Call(t, "tack_create_issue", arguments)
	id, err := uuid.Parse(created.RawID())
	if err != nil {
		t.Fatalf("parse created issue id %q: %v", created.RawID(), err)
	}
	return id
}

// rawMatchesFrom reads every raw batch from the session sort position under
// the session point in time and returns the node ID of each page match in
// order.
func rawMatchesFrom(t *testing.T, ranker *search.QueryRanker, session searchdomain.Session) []uuid.UUID {
	t.Helper()
	snapshot := session.Snapshot
	after := session.Sort
	var nodes []uuid.UUID
	for range pitTraversalPages {
		batch, err := ranker.Read(t.Context(), session.Query, snapshot, after)
		if err != nil {
			t.Fatalf("read raw batch under the session point in time: %v", err)
		}
		if len(batch.Hits) == 0 {
			return nodes
		}
		snapshot.PITID = batch.PITID
		for _, hit := range batch.Hits {
			nodes = append(nodes, hit.NodeID)
		}
		after = batch.Hits[len(batch.Hits)-1].Sort
	}
	t.Fatalf("raw matches did not end within %d batches", pitTraversalPages)
	return nil
}

// distinctNodes returns the nodes of matches in first-match order, without
// repeats and without any node of excluded.
func distinctNodes(matches, excluded []uuid.UUID) []uuid.UUID {
	nodes := make([]uuid.UUID, 0, len(matches))
	for _, id := range matches {
		if !slices.Contains(nodes, id) && !slices.Contains(excluded, id) {
			nodes = append(nodes, id)
		}
	}
	return nodes
}

// continueSearch follows the session of first to completion and returns the
// node IDs of every later page.
func continueSearch(t *testing.T, harness *MCPHarness, first searchPage) []uuid.UUID {
	t.Helper()
	var continued []uuid.UUID
	cursor := first.Cursor
	for range pitTraversalPages {
		page := callSearch(t, harness, pitMutationQuery, cursor)
		continued = append(continued, page.IDs...)
		if page.Complete {
			return continued
		}
		cursor = page.Cursor
	}
	t.Fatalf("the established session did not complete within %d pages", pitTraversalPages)
	return nil
}

// requireSameNodes requires got to contain each node of want exactly once and
// no other node.
func requireSameNodes(t *testing.T, label string, got, want []uuid.UUID) {
	t.Helper()
	seen := make(map[uuid.UUID]bool, len(got))
	for _, id := range got {
		if seen[id] {
			t.Fatalf("%s returned node %s twice", label, id)
		}
		if !slices.Contains(want, id) {
			t.Fatalf("%s returned node %s outside the expected set", label, id)
		}
		seen[id] = true
	}
	if len(seen) != len(want) {
		t.Fatalf("%s returned %d nodes, want %d", label, len(seen), len(want))
	}
}
