package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	traceHits        = 1000
	traceTopNodes    = 5
	traceQueryTokens = 40
	traceChunkTokens = 8
)

func (r *rankingRun) trace(t *testing.T, page rankingPage) {
	t.Helper()
	for position, nodeID := range page.IDs[:min(traceTopNodes, len(page.IDs))] {
		t.Logf("trace public rank=%d name=%q", position+1, r.names[nodeID])
	}
	if page.Cursor == "" {
		t.Log("trace stopped because the first page completed the session")
		return
	}
	session := r.session(t, page.Cursor)
	index := session.Query.Index
	weights := decodeTraceWeights(t, session.Snapshot.QueryTokens)
	t.Logf("trace index=%s query_tokens=%d top=%s", index, len(weights), topTraceWeights(weights, traceQueryTokens))
	lexical := encodeTrace(t, traceMatch{MultiMatch: traceMultiMatch{Query: r.query, Fields: []string{"name^3", "page_text"}}})
	sparse := encodeTrace(t, traceSparse{Nested: traceSparseNested{
		Path: "page_text_semantic_info.chunks", ScoreMode: "max",
		Query: traceNeural{NeuralSparse: map[string]traceTokens{
			"page_text_semantic_info.chunks.embedding": {QueryTokens: session.Snapshot.QueryTokens},
		}},
	}})
	filters := traceFilters(t, session.Query.Access)
	r.traceClause(t, index, "lexical", filters, lexical)
	r.traceClause(t, index, "sparse", filters, sparse)
	r.traceChunks(t, index, r.target, weights)
	if len(page.IDs) > 0 && page.IDs[0] != r.target {
		r.traceChunks(t, index, page.IDs[0], weights)
	}
	explained := append([]uuid.UUID{r.target}, page.IDs[:min(3, len(page.IDs))]...)
	r.traceExplain(t, index, filters, []json.RawMessage{lexical, sparse}, explained)
}

func (r *rankingRun) traceClause(t *testing.T, index, label string, filters []json.RawMessage, clause json.RawMessage) {
	t.Helper()
	body := traceSearch{Size: traceHits, Explain: false, Source: []string{"node_id"}, Query: traceBool{Bool: traceBoolClauses{
		Filter: filters, Should: []json.RawMessage{clause}, MinimumShouldMatch: 1,
	}}}
	ranked := rankedTraceNodes(t, r.traceSearch(t, index, body))
	targetRank, targetScore := 0, float32(0)
	top := make([]string, 0, traceTopNodes)
	for position, ranking := range ranked {
		if ranking.ID == r.target {
			targetRank, targetScore = position+1, ranking.Score
		}
		if position < traceTopNodes {
			top = append(top, fmt.Sprintf("%q=%.4f", r.names[ranking.ID], ranking.Score))
		}
	}
	t.Logf("trace clause=%s nodes=%d target_rank=%d target_score=%.4f top=[%s]",
		label, len(ranked), targetRank, targetScore, strings.Join(top, ", "))
}

func (r *rankingRun) traceExplain(t *testing.T, index string, filters, clauses []json.RawMessage, nodes []uuid.UUID) {
	t.Helper()
	identifiers := make([]string, 0, len(nodes))
	for _, nodeID := range nodes {
		identifiers = append(identifiers, nodeID.String())
	}
	selected := append(append([]json.RawMessage(nil), filters...), encodeTrace(t, traceTerms{Terms: map[string][]string{"node_id": identifiers}}))
	body := traceSearch{Size: traceHits, Explain: true, Source: []string{"node_id"}, Query: traceBool{Bool: traceBoolClauses{
		Filter: selected, Should: clauses, MinimumShouldMatch: 1,
	}}}
	for _, hit := range r.traceSearch(t, index, body) {
		t.Logf("trace explain name=%q score=%.4f parts=[%s]", r.names[traceHitNode(t, hit)], hit.Score, explanationParts(hit.Explanation, 3))
	}
}

func (r *rankingRun) traceChunks(t *testing.T, index string, nodeID uuid.UUID, weights map[string]float64) {
	t.Helper()
	filters := []json.RawMessage{
		encodeTrace(t, traceTerms{Terms: map[string][]string{"node_id": {nodeID.String()}}}),
		json.RawMessage(`{"term":{"retired":false}}`),
	}
	body := traceSearch{Size: traceHits, Explain: false, Source: []string{"page_ordinal", "page_text_semantic_info"}, Query: traceBool{Bool: traceBoolClauses{
		Filter: filters, Should: []json.RawMessage{}, MinimumShouldMatch: 0,
	}}}
	for _, hit := range r.traceSearch(t, index, body) {
		var page struct {
			PageOrdinal int `json:"page_ordinal"`
			Semantic    struct {
				Chunks []struct {
					Text      string             `json:"text"`
					Embedding map[string]float64 `json:"embedding"`
				} `json:"chunks"`
			} `json:"page_text_semantic_info"`
		}
		if err := json.Unmarshal(hit.Source, &page); err != nil {
			t.Fatalf("decode chunks of page %s: %v", hit.ID, err)
		}
		for number, chunk := range page.Semantic.Chunks {
			t.Logf("trace chunk name=%q page=%d chunk=%d dot=%.4f text=%q top=%s", r.names[nodeID], page.PageOrdinal, number,
				traceDot(weights, chunk.Embedding), chunk.Text, topTraceWeights(chunk.Embedding, traceChunkTokens))
		}
	}
}

func (r *rankingRun) traceSearch(t *testing.T, index string, body traceSearch) []opensearchapi.SearchHit {
	t.Helper()
	response, err := r.client.Search(t.Context(), &opensearchapi.SearchReq{
		Indices: []string{index}, Body: bytes.NewReader(encodeTrace(t, body)),
	})
	if err != nil {
		t.Fatalf("trace search on %s: %v", index, err)
	}
	return response.Hits.Hits
}

func traceFilters(t *testing.T, filter searchdomain.AccessFilter) []json.RawMessage {
	t.Helper()
	return []json.RawMessage{
		encodeTrace(t, traceTerm{Term: map[string]string{"access.versions": filter.Version}}),
		encodeTrace(t, traceTerms{Terms: map[string][]string{"access.keys": filter.Keys}}),
		json.RawMessage(`{"term":{"retired":false}}`),
	}
}
