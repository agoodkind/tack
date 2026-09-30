package integration

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// TestSearchSemanticPairs requires the approved short and boundary targets
// before and after a full replacement, with the original plausible distractors.
func TestSearchSemanticPairs(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	entryID := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	pairs := semanticPairs(t)
	targets := make(map[string]uuid.UUID, len(pairs))
	for _, item := range semanticCorpus(t) {
		targets[item.Identifier] = putOpaqueNode(t, fixture, kind, entryID, item.Text, item.Text, "excluded")
	}
	drainSearchWork(t, fixture.Worker, 5000)
	requireSemanticTargets(t, fixture, pairs, targets, "initial")
	request := searchdomain.BeginRebuild{Mode: searchdomain.ReplacementFull, PrimaryShards: 1, RoutingShards: 24, Reason: "relevance repeat"}
	rebuild := beginRebuild(t, fixture, request)
	runRebuildUntil(t, fixture, rebuildFinished)
	requireServing(t, fixture, rebuild.TargetIndex, fixture.Index)
	fixture.Index = rebuild.TargetIndex
	requireSemanticTargets(t, fixture, pairs, targets, "rebuilt")
}

func requireSemanticTargets(t *testing.T, fixture queryFixture, pairs []semanticPair, targets map[string]uuid.UUID, phase string) {
	t.Helper()
	for _, pair := range pairs {
		for attempt := range 3 {
			page := callSearch(t, fixture.Harness, pair.Query, "")
			rank := slices.Index(page.IDs, targets[pair.Identifier]) + 1
			if rank == 0 {
				t.Fatalf("attempt %d of %q did not return %q in the first page", attempt+1, pair.Query, pair.Target)
			}
			t.Logf("semantic phase=%s target=%s query=%q attempt=%d rank=%d", phase, pair.Identifier, pair.Query, attempt+1, rank)
		}
	}
	workspace := fixture.Workspaces[0]
	filter := callerAccess(t, fixture, workspace, entryPoint(t, fixture, workspace))
	lexicalMisses := 0
	for _, pair := range pairs {
		if !slices.Contains(lexicalTopNodes(t, fixture, filter, pair.Query), targets[pair.Identifier]) {
			lexicalMisses++
		}
	}
	if lexicalMisses == 0 {
		t.Fatal("the lexical-only control retrieved every pair")
	}
	t.Logf("semantic phase=%s lexical_misses=%d targets=%d", phase, lexicalMisses, len(pairs))
}
