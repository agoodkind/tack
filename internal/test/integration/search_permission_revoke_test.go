package integration

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/adapters/postgres"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// corruptIndexedAccess rewrites the access object of every active page of
// nodeID to the caller filter through the typed bulk API.
func corruptIndexedAccess(t *testing.T, fixture queryFixture, nodeID uuid.UUID, filter searchdomain.AccessFilter) {
	t.Helper()
	pages := searchNodePages(t, fixture.Client, fixture.Index, nodeID, false)
	if len(pages) == 0 {
		t.Fatalf("node %s has no indexed pages", nodeID)
	}
	var body strings.Builder
	for _, page := range pages {
		header := encodeNativeJSON(t, map[string]nativeBulkTarget{"update": {Index: fixture.Index, ID: page.ID}})
		access := nativeAccess{Versions: []string{filter.Version}, Keys: filter.Keys, Generation: page.Access.Generation}
		update := encodeNativeJSON(t, nativeAccessUpdateBody{Script: nativeAccessUpdateScript{
			Source: "ctx._source.access = params.access", Lang: "painless",
			Params: nativeAccessDocument{SearchGeneration: int(pageGeneration(t, page)), Access: access},
		}})
		body.WriteString(string(header) + "\n" + string(update) + "\n")
	}
	requireBulkSucceeded(t, nativeBulk(t, fixture.Client, body.String()))
	if _, err := fixture.Client.Indices.Refresh(t.Context(), &opensearchapi.IndicesRefreshReq{Index: []string{fixture.Index}}); err != nil {
		t.Fatalf("refresh %s: %v", fixture.Index, err)
	}
}

// TestSearchRevokedAccessRejectsLaterResultsAndReplay requires later pages
// and replays of an open session to omit each node that moves to a sibling
// entry point. After the caller's membership is revoked, every cursor of the
// session must fail.
func TestSearchRevokedAccessRejectsLaterResultsAndReplay(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	caller, sibling := fixture.Workspaces[0], fixture.Workspaces[1]
	if caller.OrgID != sibling.OrgID {
		t.Fatal("the fixture needs two entry points in one organization")
	}
	callerEntry, siblingEntry := entryPoint(t, fixture, caller), entryPoint(t, fixture, sibling)
	kind := putOpaqueKind(t, fixture, caller.OrgID)
	nodes := make([]uuid.UUID, 0, 30)
	for number := range 30 {
		nodes = append(nodes, putOpaqueNode(t, fixture, kind, callerEntry, fmt.Sprintf("Cobalt ledger %d", number), "cobalt ledger entry", "excluded"))
	}
	drainSearchWork(t, fixture.Worker, 2000)
	first := callSearch(t, fixture.Harness, "cobalt ledger", "")
	if first.Complete || len(first.IDs) == 0 {
		t.Fatalf("first page returned %d results and complete=%t", len(first.IDs), first.Complete)
	}
	var later uuid.UUID
	for _, candidate := range nodes {
		if !slices.Contains(first.IDs, candidate) {
			later = candidate
			break
		}
	}
	moveOpaqueNode(t, fixture, kind, later, callerEntry, siblingEntry)
	second := callSearch(t, fixture.Harness, "cobalt ledger", first.Cursor)
	if slices.Contains(second.IDs, later) {
		t.Fatalf("a later page returned node %s after it left the entry point", later)
	}
	if len(second.IDs) == 0 {
		t.Fatal("the second page returned no node")
	}
	moveOpaqueNode(t, fixture, kind, second.IDs[0], callerEntry, siblingEntry)
	replayed := callSearch(t, fixture.Harness, "cobalt ledger", first.Cursor)
	if slices.Contains(replayed.IDs, second.IDs[0]) || len(replayed.IDs) != len(second.IDs)-1 {
		t.Fatalf("replay returned %v, want %v without %s", replayed.IDs, second.IDs, second.IDs[0])
	}
	pool, err := postgres.NewPool(t.Context(), fixture.Config.DatabaseURL, nil)
	if err != nil {
		t.Fatalf("open ledger pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.NewOrgMemberRepo(pool).RemoveMember(t.Context(), caller.OrgID, caller.Actors[0].UserID); err != nil {
		t.Fatalf("remove membership: %v", err)
	}
	for _, cursor := range []string{first.Cursor, second.Cursor} {
		if _, err := trySearch(fixture.Harness, "cobalt ledger", cursor); err == nil {
			t.Fatalf("a revoked caller continued the session with cursor %q", cursor)
		}
	}
}
