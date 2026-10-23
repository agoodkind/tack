package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"goodkind.io/tack/internal/test/integration"
)

const (
	// sessionQuery supplies the matching text and public query for the session
	// inference test.
	sessionQuery = "velvet harbor"
	// sessionNodes sets the matching corpus size for the session inference test.
	sessionNodes = 80
	// sessionSlices bounds indexing work at ten worker slices per corpus node
	// plus one hundred fixture slices.
	sessionSlices = 10*sessionNodes + 100
	// sessionMinimumPages sets the minimum public page count required by the
	// session inference test.
	sessionMinimumPages = 3
	// sessionMinimumBatches sets the minimum ranked batch count required to check
	// query token reuse.
	sessionMinimumBatches = 2
	// sessionMaxPages bounds the public pages that followSession reads before
	// failing an incomplete session.
	sessionMaxPages = 100
	// analyzerQuery supplies the query text for the saved-token and analyzer
	// comparison.
	analyzerQuery = "signin"
	// analyzerFirstPage sets the distinct node rank limit for the analyzer
	// comparison.
	analyzerFirstPage = 25
	// analyzerHits sets the raw hit count requested by the analyzer search.
	analyzerHits = 1000
	// builtInAnalyzer selects the built-in analyzer that tokenizes query text
	// without model inference.
	builtInAnalyzer = "bert-uncased"
)

// analyzerSparseClause encodes a nested neural_sparse clause that uses
// query_text and an analyzer instead of saved query_tokens.
type analyzerSparseClause struct {
	Nested analyzerSparseNested `json:"nested"`
}

type analyzerSparseNested struct {
	Path      string               `json:"path"`
	ScoreMode string               `json:"score_mode"`
	Query     analyzerNeuralSparse `json:"query"`
}

type analyzerNeuralSparse struct {
	NeuralSparse map[string]analyzerQueryText `json:"neural_sparse"`
}

type analyzerQueryText struct {
	QueryText string `json:"query_text"`
	Analyzer  string `json:"analyzer"`
}

// TestSearchSessionInfersOnce follows a public search over 80 matching nodes
// to completion, including continuation replays. It requires at least three
// pages and two ranked batches with exactly one model predict request.
// The first batch must use the predicted map, and every later batch must
// reuse byte-identical query_tokens JSON.
func TestSearchSessionInfersOnce(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	workspace := fixture.Workspaces[0]
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	entryID := integration.EntryPoint(t, fixture, workspace)
	for number := range sessionNodes {
		integration.PutOpaqueNode(t, fixture, kind, entryID, fmt.Sprintf("Session node %d", number), sessionQuery, "excluded")
	}
	integration.DrainSearchWork(t, fixture.Worker, sessionSlices)
	harness, capture := newInferenceCaptureHarness(t, fixture)
	capture.take()
	pages := followSession(t, harness)
	exchange := capture.take()
	predicted := requireOnePrediction(t, exchange)
	if pages < sessionMinimumPages || len(exchange.Searches) < sessionMinimumBatches {
		t.Fatalf("search %q returned %d pages from %d ranked batches, want at least %d pages and %d batches",
			sessionQuery, pages, len(exchange.Searches), sessionMinimumPages, sessionMinimumBatches)
	}
	first := decodeRankedRequest(t, exchange.Searches[0]).Tokens
	requirePredictedTokens(t, first, predicted)
	for number, search := range exchange.Searches[1:] {
		if tokens := decodeRankedRequest(t, search).Tokens; !bytes.Equal(tokens, first) {
			t.Fatalf("ranked batch %d sends query tokens that differ from the first batch", number+2)
		}
	}
	t.Logf("session query=%q pages=%d ranked_batches=%d predicts=%d tokens=%d", sessionQuery, pages, len(exchange.Searches), len(exchange.Predicts), len(predicted))
}

// followSession reads sessionQuery to completion and returns its public page
// count. It replays each continuation cursor once and requires identical node
// IDs, cursor, and completion status. It fails the test if the session exceeds
// sessionMaxPages.
func followSession(t *testing.T, harness *integration.MCPHarness) int {
	t.Helper()
	cursor := ""
	for pages := 1; pages <= sessionMaxPages; pages++ {
		page := integration.CallSearch(t, harness, sessionQuery, cursor)
		if cursor != "" {
			replay := integration.CallSearch(t, harness, sessionQuery, cursor)
			if !slices.Equal(page.IDs, replay.IDs) || page.Cursor != replay.Cursor || page.Complete != replay.Complete {
				t.Fatalf("replaying the cursor of page %d returned a different page", pages)
			}
		}
		if page.Complete {
			return pages
		}
		cursor = page.Cursor
	}
	t.Fatalf("search %q did not complete within %d pages", sessionQuery, sessionMaxPages)
	return 0
}

// TestSearchSavedTokensFindAnalyzerMiss requires both signin targets on the
// first public page with saved tokens. It compares that page with a
// neural_sparse search using query_text "signin" and analyzer "bert-uncased"
// with the captured filters and lexical clause. The analyzer search must
// omit at least one target from the first 25 distinct nodes.
func TestSearchSavedTokensFindAnalyzerMiss(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	kind := integration.PutOpaqueKind(t, fixture, fixture.Workspaces[0].OrgID)
	targets := putRelevanceCorpus(t, fixture, approvedRelevanceNodes(t), []integration.OpaqueKind{kind})
	harness, capture := newInferenceCaptureHarness(t, fixture)
	capture.take()
	page := integration.CallSearch(t, harness, analyzerQuery, "")
	exchange := capture.take()
	requireTargetsOnPage(t, analyzerQuery, targets[analyzerQuery], page.IDs)
	if len(exchange.Searches) == 0 {
		t.Fatalf("search %q sent no ranked batch", analyzerQuery)
	}
	request := decodeRankedRequest(t, exchange.Searches[0])
	ranked := analyzerRanks(t, fixture, request, analyzerQuery)
	missed := 0
	for _, target := range targets[analyzerQuery] {
		rank := slices.Index(ranked, target) + 1
		t.Logf("analyzer query=%q analyzer=%s target=%s analyzer_rank=%d", analyzerQuery, builtInAnalyzer, target, rank)
		if rank == 0 || rank > analyzerFirstPage {
			missed++
		}
	}
	if missed == 0 {
		t.Fatalf("the %s analyzer query returned every %q target within the first %d nodes", builtInAnalyzer, analyzerQuery, analyzerFirstPage)
	}
}

// analyzerRanks searches the serving index with query_text and builtInAnalyzer
// instead of saved query_tokens. It preserves the captured filters, lexical
// clause, nested path, and score mode. It returns distinct node IDs in rank
// order.
func analyzerRanks(t *testing.T, fixture integration.QueryFixture, request rankedRequest, query string) []uuid.UUID {
	t.Helper()
	nested := request.Sparse.Nested
	sparse := analyzerSparseClause{Nested: analyzerSparseNested{
		Path: nested.Path, ScoreMode: nested.ScoreMode,
		Query: analyzerNeuralSparse{NeuralSparse: map[string]analyzerQueryText{}},
	}}
	for field := range nested.Query.NeuralSparse {
		sparse.Nested.Query.NeuralSparse[field] = analyzerQueryText{QueryText: query, Analyzer: builtInAnalyzer}
	}
	encoded, err := json.Marshal(sparse)
	if err != nil {
		t.Fatalf("encode analyzer sparse clause: %v", err)
	}
	body := traceSearch{Size: analyzerHits, Explain: false, Source: []string{"node_id"}, Query: traceBool{Bool: traceBoolClauses{
		Filter: request.Body.Query.Bool.Filter, Should: []json.RawMessage{request.Body.Query.Bool.Should[0], encoded}, MinimumShouldMatch: 1,
	}}}
	response, err := fixture.Client.Search(integration.WithTestOperation(t, t.Context()), &opensearchapi.SearchReq{
		Indices: []string{fixture.Index}, Body: bytes.NewReader(encodeTrace(t, body)),
	})
	if err != nil {
		t.Fatalf("analyzer search %q on %s: %v", query, fixture.Index, err)
	}
	nodes := rankedTraceNodes(t, response.Hits.Hits)
	ranked := make([]uuid.UUID, 0, len(nodes))
	for _, node := range nodes {
		ranked = append(ranked, node.ID)
	}
	return ranked
}
