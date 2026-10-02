package integration

import (
	_ "embed"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// semanticPair is one accepted query and the node text it must retrieve.
type semanticPair struct {
	Query, Target, Identifier string
}

//go:embed testdata/search_semantic_corpus.json
var semanticCorpusJSON []byte

type semanticCorpusNode struct {
	Identifier string `json:"identifier"`
	Text       string `json:"text"`
}

func semanticCorpus(t *testing.T) []semanticCorpusNode {
	t.Helper()
	var corpus []semanticCorpusNode
	if err := json.Unmarshal(semanticCorpusJSON, &corpus); err != nil {
		t.Fatalf("decode approved semantic corpus: %v", err)
	}
	return corpus
}

func semanticPairs(t *testing.T) []semanticPair {
	t.Helper()
	queries := []string{"db", "signin", "lag", "invoice", "crash", "remove user"}
	pairs := make([]semanticPair, 0, 12)
	for _, item := range semanticCorpus(t) {
		if !strings.HasPrefix(item.Identifier, "t-") {
			continue
		}
		position := int(item.Identifier[2] - '0')
		if position >= len(queries) {
			t.Fatalf("unknown target %q", item.Identifier)
		}
		pairs = append(pairs, semanticPair{Query: queries[position], Target: item.Text, Identifier: item.Identifier})
	}
	return pairs
}

type lexicalTerm struct {
	Term map[string]string `json:"term"`
}

type lexicalTerms struct {
	Terms map[string][]string `json:"terms"`
}

type lexicalMatch struct {
	MultiMatch struct {
		Query  string   `json:"query"`
		Fields []string `json:"fields"`
	} `json:"multi_match"`
}

// lexicalTopNodes runs the lexical-only control with the same access and
// retirement filters and returns the node IDs of the first 25 page matches.
func lexicalTopNodes(t *testing.T, fixture queryFixture, filter searchdomain.AccessFilter, query string) []uuid.UUID {
	t.Helper()
	var match lexicalMatch
	match.MultiMatch.Query = query
	match.MultiMatch.Fields = []string{"name^3", "page_text"}
	filters := []json.RawMessage{
		encodeLexical(t, lexicalTerm{Term: map[string]string{"access.versions": filter.Version}}),
		encodeLexical(t, lexicalTerms{Terms: map[string][]string{"access.keys": filter.Keys}}),
		json.RawMessage(`{"term":{"retired":false}}`),
	}
	body := encodeLexical(t, map[string]json.RawMessage{
		"size":  json.RawMessage(`25`),
		"query": encodeLexical(t, map[string]map[string][]json.RawMessage{"bool": {"filter": filters, "must": {encodeLexical(t, match)}}}),
	})
	response, err := fixture.Client.Search(t.Context(), &opensearchapi.SearchReq{Indices: []string{fixture.Index}, Body: strings.NewReader(string(body))})
	if err != nil {
		t.Fatalf("lexical control %q: %v", query, err)
	}
	nodes := make([]uuid.UUID, 0, len(response.Hits.Hits))
	for _, hit := range response.Hits.Hits {
		var source struct {
			NodeID string `json:"node_id"`
		}
		if err := json.Unmarshal(hit.Source, &source); err != nil {
			t.Fatalf("decode lexical hit: %v", err)
		}
		nodes = append(nodes, uuid.MustParse(source.NodeID))
	}
	return nodes
}

type lexicalValue interface {
	lexicalTerm | lexicalTerms | lexicalMatch | map[string]json.RawMessage | map[string]map[string][]json.RawMessage
}

func encodeLexical[Value lexicalValue](t *testing.T, value Value) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode lexical control: %v", err)
	}
	return encoded
}
