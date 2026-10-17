package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

const (
	rankingAnalyzer        = "bert-uncased"
	rankingExplainLogBytes = 4000
)

var rankingSpecialTokens = []string{"[CLS]", "[SEP]", "[PAD]", "[UNK]", "[MASK]"}

type rankingVariant struct {
	label  string
	sparse json.RawMessage
}

func (r *rankingRun) experiment(t *testing.T, index string, repeat int, targets []uuid.UUID, idf map[string]float64) {
	t.Helper()
	page := r.search(t, "")
	publicRanks := make([]int, 0, len(targets))
	for _, target := range targets {
		publicRanks = append(publicRanks, slices.Index(page.IDs, target)+1)
	}
	t.Logf("experiment query=%q repeat=%d variant=public first_page_ranks=%v", r.query, repeat, publicRanks)
	if page.Cursor == "" {
		t.Fatalf("the first page of %q completed the session", r.query)
	}
	session := r.session(t, page.Cursor)
	filters := traceFilters(t, session.Query.Access)
	analyzed := r.analyze(t, repeat)
	weights := map[string]float64{}
	for _, token := range analyzed {
		if weight, found := idf[token]; found && !slices.Contains(rankingSpecialTokens, token) {
			weights[token] = weight
		}
	}
	idfTokens, err := json.Marshal(weights)
	if err != nil {
		t.Fatalf("encode IDF query tokens: %v", err)
	}
	t.Logf("experiment query=%q repeat=%d analyzer_tokens=%q idf_tokens=%s", r.query, repeat, analyzed, topTraceWeights(weights, traceQueryTokens))
	variants := []rankingVariant{
		{label: "current", sparse: rankingTokenClause(t, session.Snapshot.QueryTokens)},
		{label: "analyzer", sparse: encodeTrace(t, traceTextSparse{Nested: traceTextNested{
			Path: "page_text_semantic_info.chunks", ScoreMode: "max",
			Query: traceTextNeural{NeuralSparse: map[string]traceText{
				"page_text_semantic_info.chunks.embedding": {QueryText: r.query, Analyzer: rankingAnalyzer},
			}},
		}})},
		{label: "idf", sparse: rankingTokenClause(t, idfTokens)},
	}
	for _, operator := range []string{"or", "and"} {
		lexical := encodeTrace(t, traceMatch{MultiMatch: traceMultiMatch{Query: r.query, Fields: []string{"name^3", "page_text"}, Operator: operator}})
		lexicalRanks := r.clauseRanks(t, index, filters, []json.RawMessage{lexical})
		for _, variant := range variants {
			label := variant.label + "/lexical_" + operator
			sparseRanks := r.clauseRanks(t, index, filters, []json.RawMessage{variant.sparse})
			combinedRanks := r.clauseRanks(t, index, filters, []json.RawMessage{lexical, variant.sparse})
			for _, target := range targets {
				combinedRank, combinedScore := rankOf(combinedRanks, target)
				lexicalRank, lexicalScore := rankOf(lexicalRanks, target)
				sparseRank, sparseScore := rankOf(sparseRanks, target)
				t.Logf("experiment query=%q repeat=%d variant=%s target=%q combined_rank=%d combined=%.4f lexical_rank=%d lexical=%.4f sparse_rank=%d sparse=%.4f",
					r.query, repeat, label, r.names[target], combinedRank, combinedScore, lexicalRank, lexicalScore, sparseRank, sparseScore)
			}
			top := make([]string, 0, traceTopNodes)
			for _, ranking := range combinedRanks[:min(traceTopNodes, len(combinedRanks))] {
				top = append(top, fmt.Sprintf("%q=%.4f", truncateName(r.names[ranking.ID]), ranking.Score))
			}
			t.Logf("experiment query=%q repeat=%d variant=%s combined_top=[%s]", r.query, repeat, label, strings.Join(top, ", "))
		}
	}
}

func truncateName(name string) string {
	runes := []rune(name)
	return string(runes[:min(80, len(runes))])
}

func rankingTokenClause(t *testing.T, tokens json.RawMessage) json.RawMessage {
	t.Helper()
	return encodeTrace(t, traceSparse{Nested: traceSparseNested{
		Path: "page_text_semantic_info.chunks", ScoreMode: "max",
		Query: traceNeural{NeuralSparse: map[string]traceTokens{
			"page_text_semantic_info.chunks.embedding": {QueryTokens: tokens},
		}},
	}})
}

func rankOf(ranked []rankedNode, nodeID uuid.UUID) (int, float32) {
	for position, ranking := range ranked {
		if ranking.ID == nodeID {
			return position + 1, ranking.Score
		}
	}
	return 0, 0
}

func (r *rankingRun) clauseRanks(t *testing.T, index string, filters, should []json.RawMessage) []rankedNode {
	t.Helper()
	body := traceSearch{Size: traceHits, Explain: false, Source: []string{"node_id"}, Query: traceBool{Bool: traceBoolClauses{
		Filter: filters, Should: should, MinimumShouldMatch: 1,
	}}}
	return rankedTraceNodes(t, r.traceSearch(t, index, body))
}

// analyze returns the bert-uncased tokens of the query. On the first repeat,
// it also logs the explained analyzer output with every token attribute.
func (r *rankingRun) analyze(t *testing.T, repeat int) []string {
	t.Helper()
	response, err := r.client.Indices.Analyze(t.Context(), opensearchapi.IndicesAnalyzeReq{
		Body: opensearchapi.IndicesAnalyzeBody{Analyzer: rankingAnalyzer, Text: []string{r.query}},
	})
	if err != nil {
		t.Fatalf("analyze %q with %s: %v", r.query, rankingAnalyzer, err)
	}
	tokens := make([]string, 0, len(response.Tokens))
	for _, token := range response.Tokens {
		tokens = append(tokens, token.Token)
	}
	if repeat == 1 {
		t.Logf("experiment query=%q analyzer_explain=%s", r.query, r.explainAnalyze(t))
	}
	return tokens
}

func (r *rankingRun) explainAnalyze(t *testing.T) string {
	t.Helper()
	body := encodeTrace(t, traceAnalyze{Analyzer: rankingAnalyzer, Text: r.query, Explain: true})
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/_analyze", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build analyze explain request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Client.Perform(request)
	if err != nil {
		t.Fatalf("explain analyze %q: %v", r.query, err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read analyze explain of %q: %v", r.query, err)
	}
	return string(payload[:min(rankingExplainLogBytes, len(payload))])
}
