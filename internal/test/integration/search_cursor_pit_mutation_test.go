package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
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
// deleted issue. A term read on node_id (a keyword field,
// mapping_properties.go:31) returns the old stored name of the edited issue
// under the session point in time and the new name under a new one.
func TestSearchCursorPointInTimeIgnoresLaterChanges(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	harness := fixture.Harness
	identifier := "PIT" + strconv.FormatInt(clock.Now().UnixNano()%1_000_000, 10)
	harness.Call(t, "tack_create_project", datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, Name: "Point in time project",
		Properties: datagen.NodeProperties{"identifier": json.RawMessage(strconv.Quote(identifier))},
	})
	harness.Project = identifier
	for number := range pitOriginalNodes {
		createPITIssue(t, harness, number)
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
	for number := range pitCreatedNodes {
		created = append(created, createPITIssue(t, harness, pitOriginalNodes+number))
	}
	harness.Call(t, "tack_update_issue", datagen.ToolArguments{WorkspaceReference: harness.Workspace, NodeID: edited.String(), Name: pitEditedName})
	harness.Call(t, "tack_delete_issue", datagen.ToolArguments{WorkspaceReference: harness.Workspace, NodeID: deleted.String()})
	drainSearchWork(t, fixture.Worker, 4000)

	if after := rawMatchesFrom(t, ranker, session); !slices.Equal(after, before) {
		t.Fatalf("raw matches under the session point in time changed: before %v, after %v", before, after)
	}
	requireStoredName(t, fixture, session.Snapshot.PITID, edited, pitMutationName, pitEditedName)
	opened, err := fixture.Client.PointInTime.Create(t.Context(), opensearchapi.PointInTimeCreateReq{
		Indices: []string{fixture.Index}, Header: nil, Params: opensearchapi.PointInTimeCreateParams{KeepAlive: time.Minute},
	})
	if err != nil {
		t.Fatalf("open a new point in time: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fixture.Client.PointInTime.Delete(context.WithoutCancel(t.Context()), opensearchapi.PointInTimeDeleteReq{PitID: []string{opened.PitID}})
	})
	requireStoredName(t, fixture, opened.PitID, edited, pitEditedName, pitMutationName)
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

// createPITIssue creates one corpus issue through tack_create_issue. A
// repeated name in one project creates no new issue. Each name ends with a
// three-digit number after the shared corpus name.
func createPITIssue(t *testing.T, harness *MCPHarness, number int) uuid.UUID {
	t.Helper()
	arguments := harness.projectArgs()
	arguments.Name = fmt.Sprintf("%s %03d", pitMutationName, number)
	created := harness.Call(t, "tack_create_issue", arguments)
	id, err := uuid.Parse(created.RawID())
	if err != nil {
		t.Fatalf("parse created issue id %q: %v", created.RawID(), err)
	}
	return id
}
