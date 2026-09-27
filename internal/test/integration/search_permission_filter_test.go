package integration

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/datagen"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
)

const permissionQuery = "saffron pipeline"

// permissionCorpus is allowed nodes under the caller's entry point and
// forbidden nodes with stronger lexical matches in another organization.
type permissionCorpus struct {
	Allowed, Forbidden []uuid.UUID
	Filter             searchdomain.AccessFilter
}

func otherOrganization(t *testing.T, fixture queryFixture) datagen.WorkspaceIdentity {
	t.Helper()
	for _, workspace := range fixture.Workspaces {
		if workspace.OrgID != fixture.Workspaces[0].OrgID {
			return workspace
		}
	}
	t.Fatal("the fixture has one organization")
	return datagen.WorkspaceIdentity{}
}

func putPermissionCorpus(t *testing.T, fixture queryFixture) permissionCorpus {
	t.Helper()
	caller, foreign := fixture.Workspaces[0], otherOrganization(t, fixture)
	callerEntry, foreignEntry := entryPoint(t, fixture, caller), entryPoint(t, fixture, foreign)
	callerKind, foreignKind := putOpaqueKind(t, fixture, caller.OrgID), putOpaqueKind(t, fixture, foreign.OrgID)
	corpus := permissionCorpus{Allowed: nil, Forbidden: nil, Filter: searchdomain.AccessFilter{Version: "", Keys: nil}}
	for number := range 5 {
		corpus.Allowed = append(corpus.Allowed, putOpaqueNode(t, fixture, callerKind, callerEntry,
			fmt.Sprintf("Allowed %d", number), "notes that mention a saffron pipeline once", "excluded"))
	}
	strong := strings.Repeat("Saffron pipeline saffron pipeline. ", 3)
	for number := range 60 {
		corpus.Forbidden = append(corpus.Forbidden, putOpaqueNode(t, fixture, foreignKind, foreignEntry,
			fmt.Sprintf("Saffron pipeline %d", number), strong, "excluded"))
	}
	drainSearchWork(t, fixture.Worker, 2000)
	filter, err := fixture.Stores.SearchPolicySet().Query(t.Context(), searchaccess.AccessRequest{
		Version: "", PrincipalID: caller.Actors[0].UserID, AuthorityID: caller.OrgID,
		EntryPointID: callerEntry, MemberOrganizations: []uuid.UUID{caller.OrgID},
	})
	if err != nil {
		t.Fatalf("compile caller access: %v", err)
	}
	corpus.Filter = filter
	return corpus
}

// rawRankedNodes reads every raw ranker batch of one snapshot in order.
func rawRankedNodes(t *testing.T, fixture queryFixture, filter searchdomain.AccessFilter, text string) []uuid.UUID {
	t.Helper()
	ranker := fixture.Adapter.Ranker(search.RankerSettings{KeepAlive: time.Minute, TokenBytes: 64 << 10, BatchSize: 100})
	query := searchdomain.Query{Text: text, Index: fixture.Index, NodeType: "", Access: filter}
	snapshot, err := ranker.Open(t.Context(), query)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	t.Cleanup(func() { _ = ranker.Close(t.Context(), snapshot) })
	var nodes []uuid.UUID
	var after json.RawMessage
	for range 10_000 {
		batch, err := ranker.Read(t.Context(), query, snapshot, after)
		if err != nil {
			t.Fatalf("read ranked batch: %v", err)
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
	t.Fatal("raw ranking did not end within 10000 batches")
	return nil
}

// TestSearchFiltersForbiddenPagesBeforeRanking gives forbidden pages
// stronger lexical matches and requires raw ranker output to exclude them.
func TestSearchFiltersForbiddenPagesBeforeRanking(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	corpus := putPermissionCorpus(t, fixture)
	ranked := rawRankedNodes(t, fixture, corpus.Filter, permissionQuery)
	for _, forbidden := range corpus.Forbidden {
		if slices.Contains(ranked, forbidden) {
			t.Fatalf("raw ranker output contains forbidden node %s", forbidden)
		}
	}
	for _, allowed := range corpus.Allowed {
		if !slices.Contains(ranked, allowed) {
			t.Fatalf("raw ranker output omits allowed node %s", allowed)
		}
	}
	callerEntry := entryPoint(t, fixture, fixture.Workspaces[0])
	requireCorpusOnce(t, callEverySearchPage(t, permissionQuery, fixture.Harness).IDs, corpus.Allowed, callerEntry)
}

// TestSearchFinalCheckRejectsCorruptIndexedAccess corrupts one indexed
// access key so a forbidden page passes OpenSearch, then requires the final
// FoundationDB check to withhold the node, including UUID-like queries.
func TestSearchFinalCheckRejectsCorruptIndexedAccess(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	corpus := putPermissionCorpus(t, fixture)
	forbidden := corpus.Forbidden[0]
	corruptIndexedAccess(t, fixture, forbidden, corpus.Filter)
	if !slices.Contains(rawRankedNodes(t, fixture, corpus.Filter, permissionQuery), forbidden) {
		t.Fatalf("the corrupted page %s did not pass the OpenSearch filter", forbidden)
	}
	for _, query := range []string{permissionQuery, forbidden.String()} {
		pages := callEverySearchPage(t, query, fixture.Harness)
		if slices.Contains(pages.IDs, forbidden) {
			t.Fatalf("search %q returned the forbidden node %s", query, forbidden)
		}
	}
}
