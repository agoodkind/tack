package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
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

func (r *rankingRun) trace(t *testing.T, probe rankingProbe, page rankingPage) {
	t.Helper()
	for position, nodeID := range page.IDs[:min(traceTopNodes, len(page.IDs))] {
		t.Logf("trace query=%q public rank=%d name=%q", probe.Query, position+1, r.names[nodeID])
	}
	if page.Cursor == "" {
		t.Logf("trace query=%q stopped because the first page completed the session", probe.Query)
		return
	}
	session := r.session(t, page.Cursor)
	index := session.Query.Index
	weights := decodeTraceWeights(t, session.Snapshot.QueryTokens)
	t.Logf("trace query=%q index=%s query_tokens=%d top=%s", probe.Query, index, len(weights), topTraceWeights(weights, traceQueryTokens))
	lexical := rankingLexicalClause(t, probe.Query)
	sparse := encodeTrace(t, traceSparse{Nested: traceSparseNested{
		Path: "page_text_semantic_info.chunks", ScoreMode: "max",
		Query: traceNeural{NeuralSparse: map[string]traceTokens{
			"page_text_semantic_info.chunks.embedding": {QueryTokens: session.Snapshot.QueryTokens},
		}},
	}})
	filters := traceFilters(t, session.Query.Access)
	r.traceClause(t, index, probe, "lexical", filters, lexical)
	r.traceClause(t, index, probe, "sparse", filters, sparse)
	chunked := append([]uuid.UUID(nil), probe.Targets...)
	if len(page.IDs) > 0 && !slices.Contains(chunked, page.IDs[0]) {
		chunked = append(chunked, page.IDs[0])
	}
	modelID := r.indexModel(t, index)
	for _, nodeID := range chunked {
		r.traceChunks(t, index, modelID, probe.Query, nodeID, weights)
	}
	explained := append(append([]uuid.UUID(nil), probe.Targets...), page.IDs[:min(3, len(page.IDs))]...)
	r.traceExplain(t, index, probe.Query, filters, []json.RawMessage{lexical, sparse}, explained)
}

// rankingLexicalClause builds the lexical multi_match clause that
// encodeRankRequest sends. The clause uses fields name^3 and page_text
// with the and operator.
func rankingLexicalClause(t *testing.T, query string) json.RawMessage {
	t.Helper()
	return encodeTrace(t, traceMatch{MultiMatch: traceMultiMatch{Query: query, Fields: []string{"name^3", "page_text"}, Operator: "and"}})
}

func (r *rankingRun) clauseRanks(t *testing.T, index string, filters, should []json.RawMessage) []rankedNode {
	t.Helper()
	body := traceSearch{Size: traceHits, Explain: false, Source: []string{"node_id"}, Query: traceBool{Bool: traceBoolClauses{
		Filter: filters, Should: should, MinimumShouldMatch: 1,
	}}}
	return rankedTraceNodes(t, r.traceSearch(t, index, body))
}

func (r *rankingRun) traceClause(t *testing.T, index string, probe rankingProbe, label string, filters []json.RawMessage, clause json.RawMessage) {
	t.Helper()
	ranked := r.clauseRanks(t, index, filters, []json.RawMessage{clause})
	top := make([]string, 0, traceTopNodes)
	targets := make([]string, 0, len(probe.Targets))
	for position, ranking := range ranked {
		if slices.Contains(probe.Targets, ranking.ID) {
			targets = append(targets, fmt.Sprintf("%d=%.4f", position+1, ranking.Score))
		}
		if position < traceTopNodes {
			top = append(top, fmt.Sprintf("%q=%.4f", r.names[ranking.ID], ranking.Score))
		}
	}
	t.Logf("trace query=%q clause=%s nodes=%d target_ranks=[%s] top=[%s]",
		probe.Query, label, len(ranked), strings.Join(targets, ", "), strings.Join(top, ", "))
}

func (r *rankingRun) traceExplain(t *testing.T, index, query string, filters, clauses []json.RawMessage, nodes []uuid.UUID) {
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
		t.Logf("trace query=%q explain name=%q score=%.4f parts=[%s]", query, r.names[traceHitNode(t, hit)], hit.Score, explanationParts(hit.Explanation, 3))
	}
}

func (r *rankingRun) traceChunks(t *testing.T, index, modelID, query string, nodeID uuid.UUID, weights map[string]float64) {
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
			t.Logf("trace query=%q chunk name=%q page=%d chunk=%d dot=%.4f text=%q top=%s", query, r.names[nodeID], page.PageOrdinal, number,
				traceDot(weights, chunk.Embedding), chunk.Text, topTraceWeights(chunk.Embedding, traceChunkTokens))
			r.traceRawChunk(t, modelID, query, r.names[nodeID], page.PageOrdinal, number, chunk.Text, chunk.Embedding, weights)
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
