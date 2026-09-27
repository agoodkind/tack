package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// semanticPair is one accepted query and the node text it must retrieve.
type semanticPair struct {
	Query, Target string
}

func semanticPairs() []semanticPair {
	return []semanticPair{
		{Query: "db", Target: "Database failover"},
		{Query: "signin", Target: "Authentication failure"},
		{Query: "lag", Target: "Slow request processing"},
		{Query: "invoice", Target: "Billing reconciliation"},
		{Query: "crash", Target: "Application terminated unexpectedly"},
		{Query: "remove user", Target: "Delete account"},
	}
}

// distractorTexts returns 165 plausible node texts that share no word with
// any semantic pair.
func distractorTexts() []string {
	subjects := []string{
		"Kitchen", "Garden", "Library", "Parking", "Travel", "Office", "Recipe", "Weather",
		"Music", "Holiday", "Printer", "Coffee", "Bicycle", "Painting", "Conference",
	}
	topics := []string{
		"schedule update", "supply order", "layout change", "cleaning rota", "photo archive",
		"seating chart", "color palette", "inventory count", "meeting notes", "rental booking", "tour planning",
	}
	texts := make([]string, 0, len(subjects)*len(topics))
	for _, subject := range subjects {
		for _, topic := range topics {
			texts = append(texts, subject+" "+topic)
		}
	}
	return texts
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
