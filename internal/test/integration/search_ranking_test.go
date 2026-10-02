package integration

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// TestSearchSemanticPairs requires the approved short and boundary targets
// before and after a full replacement, with the original plausible distractors.
func TestSearchSemanticPairs(t *testing.T) {
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", "30s")
	fixture := newQueryFixture(t, defaultQueryOptions())
	settings, err := config.LoadSearchQuerySettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if settings.IdleTimeout != 30*time.Second || settings.AbsoluteTimeout != 2*time.Hour {
		t.Fatal("relevance fixture did not retain its configured session deadlines")
	}
	t.Logf("relevance session idle=%s absolute=%s", settings.IdleTimeout, settings.AbsoluteTimeout)
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
	runRebuildUntil(t, fixture, func(current searchdomain.Rebuild, found bool) bool {
		return !found || current.State == searchdomain.RebuildRetiring
	})
	previous := fixture.Index
	requireServing(t, fixture, rebuild.TargetIndex, "")
	t.Logf("relevance serving switched from=%s to=%s", previous, rebuild.TargetIndex)
	fixture.Index = rebuild.TargetIndex
	requireSemanticTargets(t, fixture, pairs, targets, "rebuilt")
	runRebuildUntil(t, fixture, rebuildFinished)
	requireServing(t, fixture, rebuild.TargetIndex, previous)
	t.Logf("relevance previous index retired=%s", previous)
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
			completeRankedCursor(t, fixture, pair.Query, page.Cursor)
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

func completeRankedCursor(t *testing.T, fixture queryFixture, query, cursor string) {
	t.Helper()
	if cursor == "" {
		return
	}
	tokens := bytes.Clone(cursorSession(t, fixture, cursor).Snapshot.QueryTokens)
	for range 10_000 {
		page := callSearch(t, fixture.Harness, query, cursor)
		continued := cursorSession(t, fixture, cursor)
		if !bytes.Equal(tokens, continued.Snapshot.QueryTokens) {
			t.Fatal("relevance continuation changed its saved token map")
		}
		replay := callSearch(t, fixture.Harness, query, cursor)
		if !slices.Equal(page.IDs, replay.IDs) || page.Cursor != replay.Cursor || page.Complete != replay.Complete {
			t.Fatal("relevance continuation replay changed its result")
		}
		if page.Complete {
			t.Logf("relevance query=%q continuation_complete=true token_bytes=%d", query, len(tokens))
			return
		}
		cursor = page.Cursor
	}
	t.Fatal("relevance continuation did not complete within 10000 pages")
}
