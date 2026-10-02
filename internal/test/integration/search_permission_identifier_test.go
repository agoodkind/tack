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
// points, hierarchy parents, and child_of edges. Each query must return the
// same result lines in the same order once the second build's node IDs are
// mapped to the first build's node IDs.
func TestSearchPermissionIdentifierReplacement(t *testing.T) {
	keys := identifierTypeKeys{Container: opaqueSearchKey("n"), Nested: opaqueSearchKey("n")}
	first := runIdentifierBuild(t, 1, keys)
	second := runIdentifierBuild(t, 2, keys)
	requireDisjointPermissionIDs(t, first.PermissionIDs, second.PermissionIDs)

	replacements := make([]string, 0, 2*len(first.Allowed))
	for number, id := range second.Allowed {
		replacements = append(replacements, id.String(), first.Allowed[number].String())
	}
	mapper := strings.NewReplacer(replacements...)
	for _, query := range identifierQueries() {
		want, got := first.Lines[query], second.Lines[query]
		if len(got) != len(want) {
			t.Fatalf("query %q returned %d result lines after identifier replacement, want %d\nfirst:\n%s\nsecond:\n%s",
				query, len(got), len(want), strings.Join(want, "\n"), strings.Join(got, "\n"))
		}
		for number, line := range got {
			if mapped := mapper.Replace(line); mapped != want[number] {
				t.Fatalf("query %q result %d after identifier replacement is %q, want %q", query, number, mapped, want[number])
			}
		}
		if first.EntryReturned[query] != second.EntryReturned[query] {
			t.Fatalf("query %q returned the caller's entry-point node %t in the first build and %t in the second",
				query, first.EntryReturned[query], second.EntryReturned[query])
		}
	}
}

// identifierBuild is the observed public behavior of one corpus build. Lines
// stores each query's result lines without the caller's entry-point node. The
// identity bootstrap derives the entry-point name from its seed. The
// entry-point line differs between builds for that reason.
type identifierBuild struct {
	Allowed       []uuid.UUID
	PermissionIDs []uuid.UUID
	Lines         map[string][]string
	EntryReturned map[string]bool
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
		Lines: map[string][]string{}, EntryReturned: map[string]bool{},
	}
	caller := fixture.Workspaces[0]
	filter := callerAccess(t, fixture, caller, corpus.Entry)
	for _, query := range identifierQueries() {
		t.Logf("identifier build %d: checking raw ranking and tack_search results for %q", buildNumber, query)
		requireSeparatedScores(t, fixture, filter, query, corpus.Allowed)
		lines := identifierSearchLines(t, fixture.Harness, query)
		ids := make([]uuid.UUID, 0, len(lines))
		kept := make([]string, 0, len(lines))
		for _, line := range lines {
			id := resultLineID(t, line)
			ids = append(ids, id)
			if id == corpus.Entry {
				build.EntryReturned[query] = true
				continue
			}
			kept = append(kept, line)
		}
		requireCorpusOnce(t, ids, corpus.Allowed, corpus.Entry)
		build.Lines[query] = kept
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

// resultLineID parses the raw node ID at the end of one result line.
func resultLineID(t *testing.T, line string) uuid.UUID {
	t.Helper()
	marker := strings.LastIndex(line, "Raw id: `")
	if marker < 0 {
		t.Fatalf("result line %q has no raw id", line)
	}
	id, err := uuid.Parse(strings.TrimSuffix(line[marker+len("Raw id: `"):], "`"))
	if err != nil {
		t.Fatalf("parse result line %q: %v", line, err)
	}
	return id
}

// rankedScores reads every raw ranker batch of one snapshot and returns the
// best score of each node. The first sort value of a hit is its score.
func rankedScores(t *testing.T, fixture queryFixture, filter searchdomain.AccessFilter, text string) map[uuid.UUID]float64 {
	t.Helper()
	ranker := fixture.Adapter.Ranker(search.RankerSettings{KeepAlive: time.Minute, TokenBytes: 64 << 10, BatchSize: 100})
	query := searchdomain.Query{Text: text, Index: fixture.Index, NodeType: "", Access: filter}
	snapshot, err := ranker.Open(t.Context(), query)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	t.Cleanup(func() { _ = ranker.Close(t.Context(), snapshot) })
	scores := make(map[uuid.UUID]float64)
	var after json.RawMessage
	for range 1000 {
		batch, err := ranker.Read(t.Context(), query, snapshot, after)
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
