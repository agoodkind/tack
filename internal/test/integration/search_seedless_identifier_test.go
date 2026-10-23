package integration_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/test/integration"
)

const seedlessWorkSlices = 2000

type seedlessTypeFilter int

const (
	anyType seedlessTypeFilter = iota
	containerType
	nestedType
)

func (k seedlessKeys) filter(kind seedlessTypeFilter) string {
	if kind == containerType {
		return k.Container
	}
	if kind == nestedType {
		return k.Nested
	}
	return ""
}

type seedlessQuery struct {
	Text    string
	Type    seedlessTypeFilter
	Present []string
	Absent  []string
}

// seedlessQueries defines required and forbidden results for included text,
// default text, select option labels, name-only nodes, and the excluded
// phrase. It also covers type filters on container and nested types.
func seedlessQueries() []seedlessQuery {
	included, named := "saffron pipeline granite harbor", "velvet glacier beacon"
	return []seedlessQuery{
		{Text: included, Type: anyType, Present: []string{"Harbor lantern record", "Granite meadow record", "Saffron nested entry", "Pipeline nested entry"}, Absent: nil},
		{Text: "cobalt falcon juniper", Type: anyType, Present: []string{"Harbor lantern record", "Granite meadow record", "Saffron nested entry"}, Absent: nil},
		{Text: "willow ember", Type: anyType, Present: []string{"Harbor lantern record", "Saffron nested entry"}, Absent: nil},
		{Text: named, Type: anyType, Present: []string{"Velvet glacier beacon", "Velvet glacier nested"}, Absent: nil},
		{Text: seedlessExcludedText, Type: anyType, Present: []string{seedlessControlName}, Absent: []string{seedlessHolderName}},
		{Text: included, Type: nestedType, Present: []string{"Saffron nested entry", "Pipeline nested entry"}, Absent: []string{"Harbor lantern record", "Granite meadow record"}},
		{Text: named, Type: containerType, Present: []string{"Velvet glacier beacon"}, Absent: []string{"Velvet glacier nested"}},
		{Text: named, Type: nestedType, Present: []string{"Velvet glacier nested"}, Absent: []string{"Velvet glacier beacon"}},
	}
}

// TestSearchSeedlessIdentifierReplacement compares ordered tack_search
// results for matching corpora in two unseeded organizations with disjoint
// keys and IDs in one index. It creates corpus nodes through public MCP tools
// and repeats queries with and without a node_type filter.
func TestSearchSeedlessIdentifierReplacement(t *testing.T) {
	env := newSeedlessEnvironment(t)
	first := putSeedlessBuild(t, env)
	second := putSeedlessBuild(t, env)
	requireDisjointSeedlessBuilds(t, first, second)
	integration.DrainSearchWork(t, env.Worker, seedlessWorkSlices)
	for _, build := range []seedlessOrg{first, second} {
		requireExcludedTextNotIndexed(t, env, build)
	}
	replacement := newSeedlessReplacement(second, first)
	for _, query := range seedlessQueries() {
		t.Logf("checking tack_search %q with type filter %d in both organizations", query.Text, query.Type)
		want := searchSeedless(t, env, first, query.Text, first.Keys.filter(query.Type))
		requireSeedlessExpectations(t, first, query, want)
		got := searchSeedless(t, env, second, query.Text, second.Keys.filter(query.Type))
		requireSeedlessExpectations(t, second, query, got)
		replacement.requireSame(t, query.Text, got, want)
	}
}

func putSeedlessBuild(t *testing.T, env seedlessEnvironment) seedlessOrg {
	t.Helper()
	built := putSeedlessOrg(t, env, newSeedlessKeys())
	putSeedlessCorpus(t, env, built)
	return built
}

func (o seedlessOrg) identifiers() []uuid.UUID {
	ids := []uuid.UUID{o.OrgID, o.EntryID, o.UserID}
	for _, id := range o.Nodes {
		ids = append(ids, id)
	}
	return ids
}

func requireDisjointSeedlessBuilds(t *testing.T, first, second seedlessOrg) {
	t.Helper()
	for _, key := range second.Keys.all() {
		if slices.Contains(first.Keys.all(), key) {
			t.Fatalf("type, property, or option key %s exists in both organizations", key)
		}
	}
	for _, id := range second.identifiers() {
		if slices.Contains(first.identifiers(), id) {
			t.Fatalf("identifier %s exists in both organizations", id)
		}
	}
}

// requireExcludedTextNotIndexed checks indexed page text for the same phrase
// in two nodes. The control node's pages must contain the phrase from its
// included property. The holder node's pages must not contain the phrase
// from its excluded property.
func requireExcludedTextNotIndexed(t *testing.T, env seedlessEnvironment, build seedlessOrg) {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	if err := env.Adapter.Refresh(ctx, env.Index); err != nil {
		t.Fatalf("refresh search index %s: %v", env.Index, err)
	}
	for _, name := range []string{seedlessHolderName, seedlessControlName} {
		pages, err := env.Adapter.NodePageText(ctx, env.Index, build.Nodes[name])
		if err != nil || len(pages) == 0 {
			t.Fatalf("read the indexed pages of %s in organization %s: %d pages, %v", name, build.OrgID, len(pages), err)
		}
		stored := strings.Contains(strings.Join(pages, "\n"), seedlessExcludedText)
		if stored != (name == seedlessControlName) {
			t.Fatalf("indexed pages of %s in organization %s contain %q: %t", name, build.OrgID, seedlessExcludedText, stored)
		}
	}
}

func requireSeedlessExpectations(t *testing.T, build seedlessOrg, query seedlessQuery, results []seedlessResult) {
	t.Helper()
	known := map[uuid.UUID]bool{build.EntryID: true}
	for _, id := range build.Nodes {
		known[id] = true
	}
	filter := build.Keys.filter(query.Type)
	returned := map[uuid.UUID]int{}
	for _, result := range results {
		if !known[result.ID] {
			t.Fatalf("search %q in organization %s returned node %s outside the organization", query.Text, build.OrgID, result.ID)
		}
		returned[result.ID]++
		if returned[result.ID] > 1 {
			t.Fatalf("search %q in organization %s returned node %s more than once", query.Text, build.OrgID, result.ID)
		}
		if filter != "" && result.Type != filter {
			t.Fatalf("search %q filtered to type %s returned %s of type %s", query.Text, filter, result.Name, result.Type)
		}
	}
	for _, name := range query.Present {
		if returned[build.Nodes[name]] == 0 {
			t.Fatalf("search %q in organization %s omitted %s:\n%s", query.Text, build.OrgID, name, formatSeedlessResults(results))
		}
	}
	for _, name := range query.Absent {
		if returned[build.Nodes[name]] > 0 {
			t.Fatalf("search %q in organization %s returned %s:\n%s", query.Text, build.OrgID, name, formatSeedlessResults(results))
		}
	}
}

// seedlessReplacement maps the second organization's entry ID, node IDs, and
// type keys to the first organization's values for comparing result lines in
// order.
type seedlessReplacement struct {
	ids   map[uuid.UUID]uuid.UUID
	types map[string]string
}

func newSeedlessReplacement(from, to seedlessOrg) seedlessReplacement {
	replacement := seedlessReplacement{
		ids: map[uuid.UUID]uuid.UUID{from.EntryID: to.EntryID},
		types: map[string]string{
			from.Keys.Entry: to.Keys.Entry, from.Keys.Container: to.Keys.Container, from.Keys.Nested: to.Keys.Nested,
		},
	}
	for name, id := range from.Nodes {
		replacement.ids[id] = to.Nodes[name]
	}
	return replacement
}

func (r seedlessReplacement) requireSame(t *testing.T, query string, got, want []seedlessResult) {
	t.Helper()
	mapped := make([]seedlessResult, 0, len(got))
	for _, result := range got {
		mapped = append(mapped, seedlessResult{Name: result.Name, Type: r.types[result.Type], ID: r.ids[result.ID]})
	}
	if !slices.Equal(mapped, want) {
		t.Fatalf("search %q after identifier replacement returned\n%s\nwant\n%s", query, formatSeedlessResults(mapped), formatSeedlessResults(want))
	}
}

func formatSeedlessResults(results []seedlessResult) string {
	lines := make([]string, 0, len(results))
	for rank, result := range results {
		lines = append(lines, fmt.Sprintf("%d. %s (%s) %s", rank+1, result.Name, result.Type, result.ID))
	}
	return strings.Join(lines, "\n")
}
