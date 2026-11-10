package integration_test

import (
	"bytes"
	"encoding/json"
	"maps"
	"testing"
)

// rankedRequestBody defines the expected top-level fields of a production
// ranked batch request for strict decoding.
type rankedRequestBody struct {
	Size           int                `json:"size"`
	PIT            rankedRequestPIT   `json:"pit"`
	Source         []string           `json:"_source"`
	TrackTotalHits bool               `json:"track_total_hits"`
	Query          rankedRequestQuery `json:"query"`
	Sort           json.RawMessage    `json:"sort"`
	SearchAfter    json.RawMessage    `json:"search_after,omitempty"`
}

type rankedRequestPIT struct {
	ID        string `json:"id"`
	KeepAlive string `json:"keep_alive"`
}

type rankedRequestQuery struct {
	Bool rankedRequestBool `json:"bool"`
}

type rankedRequestBool struct {
	Filter             []json.RawMessage `json:"filter"`
	Should             []json.RawMessage `json:"should"`
	MinimumShouldMatch int               `json:"minimum_should_match"`
}

type rankedLexicalClause struct {
	MultiMatch rankedMultiMatch `json:"multi_match"`
}

type rankedMultiMatch struct {
	Query    string   `json:"query"`
	Fields   []string `json:"fields"`
	Operator string   `json:"operator"`
}

type rankedSparseClause struct {
	Nested rankedSparseNested `json:"nested"`
}

type rankedSparseNested struct {
	Path      string             `json:"path"`
	ScoreMode string             `json:"score_mode"`
	Query     rankedNeuralSparse `json:"query"`
}

type rankedNeuralSparse struct {
	NeuralSparse map[string]rankedQueryTokens `json:"neural_sparse"`
}

type rankedQueryTokens struct {
	QueryTokens json.RawMessage `json:"query_tokens"`
}

// rankedRequest groups a decoded batch body with its lexical clause, sparse
// clause, and original query_tokens JSON.
type rankedRequest struct {
	Body    rankedRequestBody
	Lexical rankedLexicalClause
	Sparse  rankedSparseClause
	Tokens  json.RawMessage
}

type rankedRequestPart interface {
	rankedRequestBody | rankedLexicalClause | rankedSparseClause
}

// decodeStrict decodes one ranked request part and fails the test on unknown
// fields or trailing data.
func decodeStrict[Part rankedRequestPart](t *testing.T, raw []byte) Part {
	t.Helper()
	var part Part
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&part); err != nil {
		t.Fatalf("ranked batch request part %s does not match the expected fields: %v", raw, err)
	}
	if decoder.More() {
		t.Fatalf("ranked batch request part %s has trailing data", raw)
	}
	return part
}

// decodeRankedRequest strictly decodes a captured batch and requires exactly
// two scoring clauses: multi_match followed by nested neural_sparse. It
// requires one sparse field and extracts that field's query_tokens JSON.
func decodeRankedRequest(t *testing.T, search capturedSearch) rankedRequest {
	t.Helper()
	body := decodeStrict[rankedRequestBody](t, search.Request)
	if len(body.Query.Bool.Should) != 2 {
		t.Fatalf("ranked batch has %d scoring clauses, want the lexical and sparse clauses: %s", len(body.Query.Bool.Should), search.Request)
	}
	request := rankedRequest{
		Body:    body,
		Lexical: decodeStrict[rankedLexicalClause](t, body.Query.Bool.Should[0]),
		Sparse:  decodeStrict[rankedSparseClause](t, body.Query.Bool.Should[1]),
		Tokens:  nil,
	}
	if len(request.Sparse.Nested.Query.NeuralSparse) != 1 {
		t.Fatalf("sparse clause targets %d fields, want 1: %s", len(request.Sparse.Nested.Query.NeuralSparse), search.Request)
	}
	for _, tokens := range request.Sparse.Nested.Query.NeuralSparse {
		request.Tokens = tokens.QueryTokens
	}
	return request
}

// rankedRequestShape re-encodes a ranked request after clearing its point-in-time
// ID, lexical query text, and query token map. Tests compare the remaining
// fields across queries and sessions.
func rankedRequestShape(t *testing.T, request rankedRequest) []byte {
	t.Helper()
	body := request.Body
	body.PIT.ID = ""
	lexical := request.Lexical
	lexical.MultiMatch.Query = ""
	sparse := request.Sparse
	sparse.Nested.Query.NeuralSparse = map[string]rankedQueryTokens{}
	for field := range request.Sparse.Nested.Query.NeuralSparse {
		sparse.Nested.Query.NeuralSparse[field] = rankedQueryTokens{QueryTokens: json.RawMessage("null")}
	}
	encodedLexical, err := json.Marshal(lexical)
	if err != nil {
		t.Fatalf("encode lexical clause shape: %v", err)
	}
	encodedSparse, err := json.Marshal(sparse)
	if err != nil {
		t.Fatalf("encode sparse clause shape: %v", err)
	}
	body.Query.Bool.Should = []json.RawMessage{encodedLexical, encodedSparse}
	shape, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode ranked batch shape: %v", err)
	}
	return shape
}

// requirePredictedTokens requires the ranked batch's query_tokens map to
// contain exactly the tokens and weights from the captured prediction.
func requirePredictedTokens(t *testing.T, tokens json.RawMessage, predicted map[string]float64) {
	t.Helper()
	var weights map[string]float64
	if err := json.Unmarshal(tokens, &weights); err != nil {
		t.Fatalf("decode ranked batch query tokens: %v", err)
	}
	if !maps.Equal(weights, predicted) {
		t.Fatalf("ranked batch sends %d query tokens that differ from the %d predicted tokens", len(weights), len(predicted))
	}
}
