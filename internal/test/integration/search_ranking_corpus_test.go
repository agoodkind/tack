package integration

import (
	_ "embed"
	"encoding/json"
	"fmt"
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
	response, err := fixture.Client.Search(WithTestOperation(t, t.Context()), &opensearchapi.SearchReq{Indices: []string{fixture.Index}, Body: strings.NewReader(string(body))})
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

const (
	// pageTargetRankLimit is the number of distinct nodes a page target
	// must rank within.
	pageTargetRankLimit = 25
	// pageTargetAdvance is the number of new bytes each continuation page of
	// ASCII text adds after its one-quarter overlap.
	pageTargetAdvance = runtimePageBytes - runtimePageBytes/4
	// pageTargetBoundary is a lexical target that crosses a page end.
	pageTargetBoundary = "kestrelboundary"
	// pageTargetBoundaryPrefix is the part of pageTargetBoundary that the
	// page before the boundary ends with.
	pageTargetBoundaryPrefix = "kestrel"
	// pageTargetSemantic is the final-page text the semantic query "crash"
	// retrieves without a lexical match.
	pageTargetSemantic = "Application terminated unexpectedly."
	// pageTargetSemanticQuery is the semantic query for pageTargetSemantic.
	pageTargetSemanticQuery = "crash"
	pageTargetName          = "Page position fixture"
)

// pageTarget is one query and the node it must return.
type pageTarget struct {
	Query  string
	NodeID uuid.UUID
}

// pageEnd returns the projected byte offset where ASCII page ordinal ends.
func pageEnd(ordinal int) int {
	return runtimePageBytes + ordinal*pageTargetAdvance
}

// pageFiller returns count numbered filler sentences starting at first.
func pageFiller(first, count int) string {
	var filler strings.Builder
	for number := first; number < first+count; number++ {
		_, _ = fmt.Fprintf(&filler, "routine filler line %03d. ", number)
	}
	return filler.String()
}

// padToOffset appends spaces to text until it has offset bytes.
func padToOffset(t *testing.T, text string, offset int) string {
	t.Helper()
	if len(text) > offset {
		t.Fatalf("page target text has %d bytes, beyond the planned offset %d", len(text), offset)
	}
	return text + strings.Repeat(" ", offset-len(text))
}

// putPagePositionNodes creates a node with lexical targets on its first page,
// a middle page, across a page end, and on its final page. The final page
// also contains pageTargetSemantic. The function creates a second node with
// a name and a value that each span more than three pages and end with a
// target.
func putPagePositionNodes(t *testing.T, fixture queryFixture, kind opaqueKind, entryID uuid.UUID) (uuid.UUID, []pageTarget) {
	t.Helper()
	projected := pageTargetName + "\n" + "kestrelbegin " + pageFiller(1, 10)
	projected += " kestrelmiddle " + pageFiller(11, 10)
	projected = padToOffset(t, projected, pageEnd(5)-len(pageTargetBoundaryPrefix)) + pageTargetBoundary + " " + pageFiller(21, 10)
	projected = padToOffset(t, projected, pageEnd(9)) + "kestrelfinal " + pageTargetSemantic
	positions := putOpaqueNode(t, fixture, kind, entryID, pageTargetName, projected[len(pageTargetName)+1:], "excluded")

	longName := "kestrelname " + pageFiller(31, 16) + "kestrelnameend"
	longValue := pageFiller(47, 16) + "kestrelvalueend"
	if len(longName) <= 3*runtimePageBytes || len(longValue) <= 3*runtimePageBytes {
		t.Fatalf("long name has %d bytes and value %d bytes, want each above %d", len(longName), len(longValue), 3*runtimePageBytes)
	}
	long := putOpaqueNode(t, fixture, kind, entryID, longName, longValue, "excluded")
	targets := []pageTarget{
		{Query: "kestrelbegin", NodeID: positions},
		{Query: "kestrelmiddle", NodeID: positions},
		{Query: pageTargetBoundary, NodeID: positions},
		{Query: "kestrelfinal", NodeID: positions},
		{Query: pageTargetSemanticQuery, NodeID: positions},
		{Query: "kestrelnameend", NodeID: long},
		{Query: "kestrelvalueend", NodeID: long},
	}
	return positions, targets
}
