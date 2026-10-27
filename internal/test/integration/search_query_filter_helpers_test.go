package integration_test

import (
	"bytes"
	"encoding/json"
	"testing"

	searchdomain "goodkind.io/tack/internal/domain/search"
)

type termFilter struct {
	Term map[string]string `json:"term"`
}

type termsFilter struct {
	Terms map[string][]string `json:"terms"`
}

type flagFilter struct {
	Term map[string]bool `json:"term"`
}

type filterLexicalClause struct {
	MultiMatch filterLexicalMatch `json:"multi_match"`
}

type filterLexicalMatch struct {
	Query string `json:"query"`
}

func encodeFilter[Clause termFilter | termsFilter | flagFilter](t *testing.T, clause Clause) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(clause)
	if err != nil {
		t.Fatalf("encode expected filter clause: %v", err)
	}
	return encoded
}

// expectedFilters encodes access version, access keys, and retirement
// filters in request order. It appends a type filter when nodeType is
// nonempty.
func expectedFilters(t *testing.T, filter searchdomain.AccessFilter, nodeType string) []json.RawMessage {
	t.Helper()
	clauses := []json.RawMessage{
		encodeFilter(t, termFilter{Term: map[string]string{"access.versions": filter.Version}}),
		encodeFilter(t, termsFilter{Terms: map[string][]string{"access.keys": filter.Keys}}),
		encodeFilter(t, flagFilter{Term: map[string]bool{"retired": false}}),
	}
	if nodeType != "" {
		clauses = append(clauses, encodeFilter(t, termFilter{Term: map[string]string{"node_type": nodeType}}))
	}
	return clauses
}

// requireCapturedFilters compares every batch's compact filter JSON with
// the expected clauses in order. It also requires the first should clause
// to preserve the query text in multi_match.
func requireCapturedFilters(t *testing.T, searches []capturedSearch, query string, expected []json.RawMessage) {
	t.Helper()
	if len(searches) == 0 {
		t.Fatalf("search %q sent no ranked batch", query)
	}
	for _, search := range searches {
		request := decodeCapturedRequest(t, search)
		if len(request.Query.Bool.Filter) != len(expected) {
			t.Fatalf("search %q sent %d filter clauses, want %d: %s", query, len(request.Query.Bool.Filter), len(expected), search.Request)
		}
		for position, clause := range request.Query.Bool.Filter {
			var compact bytes.Buffer
			if err := json.Compact(&compact, clause); err != nil {
				t.Fatalf("compact filter clause %s: %v", clause, err)
			}
			if !bytes.Equal(compact.Bytes(), expected[position]) {
				t.Fatalf("search %q sent filter clause %d %s, want %s", query, position, clause, expected[position])
			}
		}
		var lexical filterLexicalClause
		if len(request.Query.Bool.Should) == 0 || json.Unmarshal(request.Query.Bool.Should[0], &lexical) != nil || lexical.MultiMatch.Query != query {
			t.Fatalf("search %q did not send the query as lexical text: %s", query, search.Request)
		}
	}
}
