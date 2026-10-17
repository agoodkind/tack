package integration_test

import (
	"encoding/json"
	"testing"
)

type traceSearch struct {
	Size    int       `json:"size"`
	Explain bool      `json:"explain"`
	Source  []string  `json:"_source"`
	Query   traceBool `json:"query"`
}

type traceBool struct {
	Bool traceBoolClauses `json:"bool"`
}

type traceBoolClauses struct {
	Filter             []json.RawMessage `json:"filter"`
	Should             []json.RawMessage `json:"should"`
	MinimumShouldMatch int               `json:"minimum_should_match"`
}

type traceTerm struct {
	Term map[string]string `json:"term"`
}

type traceTerms struct {
	Terms map[string][]string `json:"terms"`
}

type traceMatch struct {
	MultiMatch traceMultiMatch `json:"multi_match"`
}

type traceMultiMatch struct {
	Query    string   `json:"query"`
	Fields   []string `json:"fields"`
	Operator string   `json:"operator,omitempty"`
}

type traceSparse struct {
	Nested traceSparseNested `json:"nested"`
}

type traceSparseNested struct {
	Path      string      `json:"path"`
	ScoreMode string      `json:"score_mode"`
	Query     traceNeural `json:"query"`
}

type traceNeural struct {
	NeuralSparse map[string]traceTokens `json:"neural_sparse"`
}

type traceTokens struct {
	QueryTokens json.RawMessage `json:"query_tokens"`
}

type traceTextSparse struct {
	Nested traceTextNested `json:"nested"`
}

type traceTextNested struct {
	Path      string          `json:"path"`
	ScoreMode string          `json:"score_mode"`
	Query     traceTextNeural `json:"query"`
}

type traceTextNeural struct {
	NeuralSparse map[string]traceText `json:"neural_sparse"`
}

type traceText struct {
	QueryText string `json:"query_text"`
	Analyzer  string `json:"analyzer"`
}

type traceAnalyze struct {
	Analyzer string `json:"analyzer"`
	Text     string `json:"text"`
	Explain  bool   `json:"explain"`
}

type traceValue interface {
	traceSearch | traceTerm | traceTerms | traceMatch | traceSparse | traceTextSparse | traceAnalyze
}

func encodeTrace[Value traceValue](t *testing.T, value Value) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode trace request: %v", err)
	}
	return encoded
}
