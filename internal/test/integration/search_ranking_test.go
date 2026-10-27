package integration

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// TestSearchSemanticPairs requires the approved short and boundary targets
// before and after a full replacement, with the original plausible distractors.
// Before the replacement, it also requires targets at the beginning, middle,
// page boundary, and final page of a multi-page node, a semantic final-page
// target, and targets at the end of a name and a value that span many pages.
func TestSearchSemanticPairs(t *testing.T) {
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", "30s")
	fixture := newQueryFixture(t, defaultQueryOptions())
	settings, err := config.LoadSearchQuerySettings(WithTestOperation(t, t.Context()))
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
	positionNode, pageTargets := putPagePositionNodes(t, fixture, kind, entryID)
	drainSearchWork(t, fixture.Worker, 5000)
	requirePagePositions(t, readSearchPages(t, fixture.Stores, positionNode, runtimePageBytes))
	requireSemanticTargets(t, fixture, pairs, targets, "initial")
	requirePageTargets(t, fixture, entryID, positionNode, pageTargets)
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

// requirePagePositions requires the reader pages of the position node to
// place each target where putPagePositionNodes planned it.
func requirePagePositions(t *testing.T, pages []node.ContentPage) {
	t.Helper()
	last := len(pages) - 1
	if last < 4 || !strings.Contains(pages[0].Text, "kestrelbegin") {
		t.Fatalf("position node has %d pages, want the beginning target on page 0 of at least 5", len(pages))
	}
	crossed := false
	for ordinal, page := range pages {
		if strings.Contains(page.Text, "kestrelmiddle") && (ordinal == 0 || ordinal == last) {
			t.Fatalf("middle target is on page %d of %d", ordinal, last)
		}
		if ordinal < last && (strings.Contains(page.Text, "kestrelfinal") || strings.Contains(page.Text, pageTargetSemantic)) {
			t.Fatalf("final targets are on page %d before the final page %d", ordinal, last)
		}
		if ordinal < last && strings.HasSuffix(page.Text, pageTargetBoundaryPrefix) && !strings.Contains(page.Text, pageTargetBoundary) &&
			strings.Contains(pages[ordinal+1].Text, pageTargetBoundary) {
			crossed = true
		}
	}
	if !crossed {
		t.Fatalf("no page ends inside %q", pageTargetBoundary)
	}
	if !strings.Contains(pages[last].Text, "kestrelfinal "+pageTargetSemantic) {
		t.Fatalf("final page %q lacks the final targets", pages[last].Text)
	}
}

// requirePageTargets requires each target node within the first 25 distinct
// nodes of its query and requires the lexical control to miss the semantic
// final-page target.
func requirePageTargets(t *testing.T, fixture queryFixture, entryID, semanticNode uuid.UUID, targets []pageTarget) {
	t.Helper()
	for _, target := range targets {
		distinct := firstDistinctNodes(t, fixture, target.Query, pageTargetRankLimit)
		rank := slices.Index(distinct, target.NodeID) + 1
		if rank == 0 {
			t.Fatalf("query %q did not return node %s within the first %d distinct nodes", target.Query, target.NodeID, pageTargetRankLimit)
		}
		t.Logf("page target query=%q node=%s rank=%d", target.Query, target.NodeID, rank)
	}
	filter := callerAccess(t, fixture, fixture.Workspaces[0], entryID)
	if slices.Contains(lexicalTopNodes(t, fixture, filter, pageTargetSemanticQuery), semanticNode) {
		t.Fatalf("the lexical-only control retrieved the semantic final-page target for %q", pageTargetSemanticQuery)
	}
}

// firstDistinctNodes follows result pages until it has limit distinct node
// IDs or the search completes.
func firstDistinctNodes(t *testing.T, fixture queryFixture, query string, limit int) []uuid.UUID {
	t.Helper()
	distinct := make([]uuid.UUID, 0, limit)
	cursor := ""
	for range 100 {
		page := callSearch(t, fixture.Harness, query, cursor)
		for _, id := range page.IDs {
			if len(distinct) < limit && !slices.Contains(distinct, id) {
				distinct = append(distinct, id)
			}
		}
		if page.Complete || len(distinct) == limit {
			return distinct
		}
		cursor = page.Cursor
	}
	t.Fatalf("search %q did not return %d distinct nodes within 100 pages", query, limit)
	return nil
}
