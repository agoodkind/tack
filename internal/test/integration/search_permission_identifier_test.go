package integration

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/search"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// TestSearchPermissionIdentifierReplacement builds the same access corpus
// twice. The second build gets new organizations, users, memberships, entry
// points, hierarchy parents, and child_of edges. Each query must return
// identical result items in identical order. The test maps the second
// build's node IDs to the first build's node IDs to compare node identities.
func TestSearchPermissionIdentifierReplacement(t *testing.T) {
	keys := identifierTypeKeys{Container: opaqueSearchKey("n"), Nested: opaqueSearchKey("n")}
	first := runIdentifierBuild(t, 1, keys)
	second := runIdentifierBuild(t, 2, keys)
	requireDisjointPermissionIDs(t, first.PermissionIDs, second.PermissionIDs)

	replacements := make(map[uuid.UUID]uuid.UUID, len(first.Allowed))
	for number, id := range second.Allowed {
		replacements[id] = first.Allowed[number]
	}
	for _, query := range identifierQueries() {
		want, got := first.Lines[query], second.Lines[query]
		if !slices.Equal(got, want) {
			t.Fatalf("query %q returned different result items after identifier replacement\nfirst:\n%s\nsecond:\n%s",
				query, strings.Join(want, "\n"), strings.Join(got, "\n"))
		}
		for number, id := range second.IDs[query] {
			if mapped := replacements[id]; mapped != first.IDs[query][number] {
				t.Fatalf("query %q result %d after identifier replacement is node %s, want %s", query, number, mapped, first.IDs[query][number])
			}
		}
	}
}

// identifierBuild records the public search results of one corpus build.
// Lines stores each query's result items without the caller's entry-point
// node. The identity bootstrap derives the entry-point name from its seed,
// and semantic ranking matches that name text. The entry-point item differs
// between builds, and a query can return the entry-point node in one build
// and not the other. The comparison leaves the entry-point node out.
type identifierBuild struct {
	Allowed       []uuid.UUID
	PermissionIDs []uuid.UUID
	Lines         map[string][]string
	IDs           map[string][]uuid.UUID
}

// runIdentifierBuild creates one fixture and corpus, then records every
// identifier query through public tack_search. It logs the build number
// before each query's checks, and a failed check then names its build.
func runIdentifierBuild(t *testing.T, buildNumber int, keys identifierTypeKeys) identifierBuild {
	t.Helper()
	fixture := newQueryFixture(t, defaultQueryOptions())
	corpus := putIdentifierCorpus(t, fixture, keys)
	build := identifierBuild{
		Allowed: corpus.Allowed, PermissionIDs: corpus.PermissionIDs,
		Lines: map[string][]string{}, IDs: map[string][]uuid.UUID{},
	}
	caller := fixture.Workspaces[0]
	filter := callerAccess(t, fixture, caller, corpus.Entry)
	for _, query := range identifierQueries() {
		t.Logf("identifier build %d: checking raw ranking and tack_search results for %q", buildNumber, query)
		requireSeparatedScores(t, fixture, filter, query, corpus.Allowed)
		lines, ids := identifierSearchLines(t, fixture.Harness, query)
		kept := make([]string, 0, len(lines))
		keptIDs := make([]uuid.UUID, 0, len(ids))
		for number, line := range lines {
			if ids[number] == corpus.Entry {
				continue
			}
			kept = append(kept, line)
			keptIDs = append(keptIDs, ids[number])
		}
		requireCorpusOnce(t, ids, corpus.Allowed, corpus.Entry)
		build.Lines[query] = kept
		build.IDs[query] = keptIDs
	}
	// The next fixture sets the public alias, which the adapter refuses while
	// the alias selects another index. Deleting this index removes the alias.
	deleteNativeIndex(t, fixture.Client, fixture.Index)
	return build
}

// requireDisjointPermissionIDs requires the second build to share no
// organization, user, entry point, or hierarchy node with the first build.
func requireDisjointPermissionIDs(t *testing.T, first, second []uuid.UUID) {
	t.Helper()
	for _, id := range second {
		if slices.Contains(first, id) {
			t.Fatalf("permission identifier %s exists in both builds", id)
		}
	}
}

// rankedScores reads every raw ranker batch of one snapshot and returns the
// best score of each node. The first sort value of a hit is its score.
func rankedScores(t *testing.T, fixture queryFixture, filter searchdomain.AccessFilter, text string) map[uuid.UUID]float64 {
	t.Helper()
	ranker := fixture.Adapter.Ranker(search.RankerSettings{KeepAlive: time.Minute, TokenBytes: 64 << 10, BatchSize: 100})
	query := searchdomain.Query{Text: text, Index: fixture.Index, NodeType: "", Access: filter}
	snapshot, err := ranker.Open(WithTestOperation(t, t.Context()), query)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	t.Cleanup(func() { _ = ranker.Close(WithTestOperation(t, t.Context()), snapshot) })
	scores := make(map[uuid.UUID]float64)
	var after json.RawMessage
	for range 1000 {
		batch, err := ranker.Read(WithTestOperation(t, t.Context()), query, snapshot, after)
		if err != nil {
			t.Fatalf("read ranked batch: %v", err)
		}
		if len(batch.Hits) == 0 {
			return scores
		}
		snapshot.PITID = batch.PITID
		for _, hit := range batch.Hits {
			var values []json.RawMessage
			if err := json.Unmarshal(hit.Sort, &values); err != nil || len(values) == 0 {
				t.Fatalf("decode sort values %s: %v", hit.Sort, err)
			}
			score, err := strconv.ParseFloat(string(values[0]), 64)
			if err != nil {
				t.Fatalf("parse score %s: %v", values[0], err)
			}
			if _, seen := scores[hit.NodeID]; !seen {
				scores[hit.NodeID] = score
			}
		}
		after = batch.Hits[len(batch.Hits)-1].Sort
	}
	t.Fatal("raw ranking did not end within 1000 batches")
	return nil
}
