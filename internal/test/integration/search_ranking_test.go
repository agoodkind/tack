package integration

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/searchaccess"
)

// TestSearchSemanticPairs indexes six accepted targets beside 165
// distractors. Each query must return its target within the first 25
// distinct nodes three times, and the lexical-only control must miss at
// least one pair that the combined query retrieves.
func TestSearchSemanticPairs(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	entryID := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	targets := make(map[string]uuid.UUID, len(semanticPairs()))
	for _, pair := range semanticPairs() {
		targets[pair.Query] = putOpaqueNode(t, fixture, kind, entryID, pair.Target, pair.Target, "excluded")
	}
	distractors := distractorTexts()
	if len(distractors) < 150 {
		t.Fatalf("corpus has %d distractors, want at least 150", len(distractors))
	}
	for _, text := range distractors {
		putOpaqueNode(t, fixture, kind, entryID, text, text, "excluded")
	}
	drainSearchWork(t, fixture.Worker, 5000)
	for _, pair := range semanticPairs() {
		for attempt := range 3 {
			page := callSearch(t, fixture.Harness, pair.Query, "")
			if !slices.Contains(page.IDs, targets[pair.Query]) {
				t.Fatalf("attempt %d of %q did not return %q in the first page", attempt+1, pair.Query, pair.Target)
			}
		}
	}
	filter, err := fixture.Stores.SearchPolicySet().Query(t.Context(), searchaccess.AccessRequest{
		Version: "", PrincipalID: workspace.Actors[0].UserID, AuthorityID: workspace.OrgID,
		EntryPointID: entryID, MemberOrganizations: []uuid.UUID{workspace.OrgID},
	})
	if err != nil {
		t.Fatalf("compile caller access: %v", err)
	}
	lexicalMisses := 0
	for _, pair := range semanticPairs() {
		if !slices.Contains(lexicalTopNodes(t, fixture, filter, pair.Query), targets[pair.Query]) {
			lexicalMisses++
		}
	}
	if lexicalMisses == 0 {
		t.Fatal("the lexical-only control retrieved every pair")
	}
}
