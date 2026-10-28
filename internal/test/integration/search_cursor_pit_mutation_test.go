package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
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

// TestSearchCursorPointInTimeIgnoresLaterChanges opens a search session and
// records the title of every raw engine match after the session sort position.
// It then creates, edits, and deletes issues with the MCP tools and indexes each
// change. It checks that the session snapshot returns the original raw matches
// and that continuation returns each remaining node except the deleted issue.
// It checks that the original snapshot retains the edited issue's old name and
// a new snapshot excludes that name. Fresh searches must return the created
// issues and the edited name and omit the deleted issue.
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
	registerSearchNodes(t, fixture, before)
	remaining := distinctNodes(before, first.IDs)
	if len(remaining) < 2 {
		t.Fatalf("raw matches after the first page name %d nodes, want at least two", len(remaining))
	}
	edited, deleted := remaining[0], remaining[1]
	// A deferred call runs after t.Fatalf while t.Context is still open.
	// Cleanup functions run after t.Context is canceled.
	defer func() {
		if t.Failed() {
			logLiveWorkAndPages(t, fixture, edited)
		}
	}()

	created := make([]uuid.UUID, 0, pitCreatedNodes)
	for number := range pitCreatedNodes {
		created = append(created, createPITIssue(t, harness, pitOriginalNodes+number))
	}
	harness.Call(t, "tack_update_issue", datagen.ToolArguments{WorkspaceReference: harness.Workspace, NodeID: edited.String(), Name: pitEditedName})
	harness.Call(t, "tack_delete_issue", datagen.ToolArguments{WorkspaceReference: harness.Workspace, NodeID: deleted.String()})
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	drainSearchWork(t, fixture.Worker, 4000)
	slog.SetDefault(previous)

	if after := rawMatchesFrom(t, ranker, session); !slices.Equal(after, before) {
		t.Fatalf("raw matches under the session point in time changed: before %v, after %v", before, after)
	}
	requireStoredName(t, fixture, session.Snapshot.PITID, edited, pitMutationName, pitEditedName)
	current := readSearchPages(t, fixture.Stores, edited, runtimePageBytes)
	t.Logf("renamed issue %s current revision %s", edited, current[0].Revision)
	opened, err := ranker.Open(WithTestOperation(t, t.Context()), session.Query)
	if err != nil {
		t.Fatalf("open a new session snapshot: %v", err)
	}
	t.Cleanup(func() {
		if err := ranker.Close(context.WithoutCancel(WithTestOperation(t, t.Context())), opened); err != nil {
			t.Errorf("close the new session snapshot: %v", err)
		}
	})
	requireStoredName(t, fixture, opened.PITID, edited, pitEditedName, pitMutationName)
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
