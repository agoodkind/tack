package integration_test

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/test/integration"
)

const (
	// relevanceOpaqueKinds sets the number of generated node types that share
	// the approved relevance corpus.
	relevanceOpaqueKinds = 3
	// relevanceCorpusSlices bounds the worker slices that drain corpus indexing.
	relevanceCorpusSlices = 5000
	// relevanceTargetsPerQuery sets the required target count for each approved
	// query in the corpus.
	relevanceTargetsPerQuery = 2
)

// approvedRelevanceNodes decodes the embedded approved semantic corpus and
// fails the test if decoding fails.
func approvedRelevanceNodes(t *testing.T) []rankingApprovedNode {
	t.Helper()
	var nodes []rankingApprovedNode
	if err := json.Unmarshal(rankingApprovedJSON, &nodes); err != nil {
		t.Fatalf("decode approved semantic corpus: %v", err)
	}
	return nodes
}

// putRelevanceCorpus creates nodes in the supplied order, assigns the supplied
// node types in turn, and uses each corpus text as the name and included
// property. It indexes the nodes and returns target IDs by approved query,
// requiring two targets per query.
func putRelevanceCorpus(t *testing.T, fixture integration.QueryFixture, nodes []rankingApprovedNode, kinds []integration.OpaqueKind) map[string][]uuid.UUID {
	t.Helper()
	entryID := integration.EntryPoint(t, fixture, fixture.Workspaces[0])
	targets := make(map[string][]uuid.UUID, len(rankingApprovedQueries))
	for position, item := range nodes {
		nodeID := integration.PutOpaqueNode(t, fixture, kinds[position%len(kinds)], entryID, item.Text, item.Text, "excluded")
		if !strings.HasPrefix(item.Identifier, "t-") {
			continue
		}
		query := rankingApprovedQueries[int(item.Identifier[2]-'0')]
		targets[query] = append(targets[query], nodeID)
	}
	for _, query := range rankingApprovedQueries {
		if len(targets[query]) != relevanceTargetsPerQuery {
			t.Fatalf("approved corpus has %d targets for %q, want %d", len(targets[query]), query, relevanceTargetsPerQuery)
		}
	}
	integration.DrainSearchWork(t, fixture.Worker, relevanceCorpusSlices)
	return targets
}

// requireTargetsOnPage requires every target on the supplied first public
// page and logs each target's rank.
func requireTargetsOnPage(t *testing.T, query string, targets, page []uuid.UUID) {
	t.Helper()
	for _, target := range targets {
		rank := slices.Index(page, target) + 1
		if rank == 0 {
			t.Fatalf("search %q did not return target %s on the first page of %d nodes", query, target, len(page))
		}
		t.Logf("relevance query=%q target=%s rank=%d", query, target, rank)
	}
}

// TestSearchRelevanceOpaqueCorpus checks all six approved queries against
// shuffled corpus nodes with generated type and property keys. Each query
// must return both targets on its first public page and send one model predict
// request. Its first ranked batch must preserve the lexical query, omit
// search_after, and use the predicted tokens. After clearing the point-in-time
// ID, query text, and token map, every first batch must match the first query's
// request shape.
func TestSearchRelevanceOpaqueCorpus(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	seed := rand.Uint64()
	t.Logf("relevance corpus shuffle seed=%d", seed)
	nodes := approvedRelevanceNodes(t)
	rand.New(rand.NewPCG(seed, seed)).Shuffle(len(nodes), func(i, j int) { nodes[i], nodes[j] = nodes[j], nodes[i] })
	kinds := make([]integration.OpaqueKind, 0, relevanceOpaqueKinds)
	for range relevanceOpaqueKinds {
		kinds = append(kinds, integration.PutOpaqueKind(t, fixture, fixture.Workspaces[0].OrgID))
	}
	targets := putRelevanceCorpus(t, fixture, nodes, kinds)
	harness, capture := newInferenceCaptureHarness(t, fixture)
	var firstShape []byte
	for _, query := range rankingApprovedQueries {
		capture.take()
		page := integration.CallSearch(t, harness, query, "")
		exchange := capture.take()
		requireTargetsOnPage(t, query, targets[query], page.IDs)
		predicted := requireOnePrediction(t, exchange)
		if len(exchange.Searches) == 0 {
			t.Fatalf("search %q sent no ranked batch", query)
		}
		request := decodeRankedRequest(t, exchange.Searches[0])
		if request.Lexical.MultiMatch.Query != query {
			t.Fatalf("ranked batch for %q sends lexical text %q", query, request.Lexical.MultiMatch.Query)
		}
		if len(request.Body.SearchAfter) != 0 {
			t.Fatalf("the first ranked batch for %q sends search_after %s", query, request.Body.SearchAfter)
		}
		requirePredictedTokens(t, request.Tokens, predicted)
		shape := rankedRequestShape(t, request)
		if firstShape == nil {
			firstShape = shape
			t.Logf("relevance request shape=%s", shape)
			continue
		}
		if !bytes.Equal(shape, firstShape) {
			t.Fatalf("ranked batch for %q differs from %q apart from query text, token map, and point in time:\n%s\n%s", query, rankingApprovedQueries[0], shape, firstShape)
		}
	}
}
