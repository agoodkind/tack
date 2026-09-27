package search

import (
	"encoding/json"
	"strconv"
	"time"

	searchdomain "goodkind.io/tack/internal/domain/search"
)

// nameBoost weights lexical name matches above page text matches.
const nameBoost = "name^3"

// rankSort orders page matches by score, then node ID, then the shard
// document position that the point in time keeps stable.
var rankSort = json.RawMessage(`[{"_score":{"order":"desc"}},{"node_id":{"order":"asc"}},{"_shard_doc":{"order":"asc"}}]`)

type rankRequestBody struct {
	Size           int             `json:"size"`
	PIT            rankPIT         `json:"pit"`
	Source         []string        `json:"_source"`
	TrackTotalHits bool            `json:"track_total_hits"`
	Query          rankQuery       `json:"query"`
	Sort           json.RawMessage `json:"sort"`
	SearchAfter    json.RawMessage `json:"search_after,omitempty"`
}

type rankPIT struct {
	ID        string `json:"id"`
	KeepAlive string `json:"keep_alive"`
}

type rankQuery struct {
	Bool rankBool `json:"bool"`
}

type rankBool struct {
	Filter             []json.RawMessage `json:"filter"`
	Should             []json.RawMessage `json:"should"`
	MinimumShouldMatch int               `json:"minimum_should_match"`
}

type termClause struct {
	Term map[string]string `json:"term"`
}

type termsClause struct {
	Terms map[string][]string `json:"terms"`
}

type booleanTermClause struct {
	Term map[string]bool `json:"term"`
}

type multiMatchClause struct {
	MultiMatch multiMatch `json:"multi_match"`
}

type multiMatch struct {
	Query  string   `json:"query"`
	Fields []string `json:"fields"`
}

type nestedClause struct {
	Nested nestedSparse `json:"nested"`
}

type nestedSparse struct {
	Path      string           `json:"path"`
	ScoreMode string           `json:"score_mode"`
	Query     neuralSparseSpec `json:"query"`
}

type neuralSparseSpec struct {
	NeuralSparse map[string]sparseTokens `json:"neural_sparse"`
}

type sparseTokens struct {
	QueryTokens json.RawMessage `json:"query_tokens"`
}

// encodeRankRequest builds one filtered native sparse search body. The
// access, retirement, and node type filters apply before scoring.
func encodeRankRequest(query searchdomain.Query, snapshot searchdomain.Snapshot, after json.RawMessage, size int, keepAlive time.Duration) ([]byte, error) {
	filters, err := encodeFilters(query)
	if err != nil {
		return nil, err
	}
	lexical, err := json.Marshal(multiMatchClause{MultiMatch: multiMatch{Query: query.Text, Fields: []string{nameBoost, "page_text"}}})
	if err != nil {
		return nil, queryStepError{operation: "encode lexical clause", err: err}
	}
	semantic, err := json.Marshal(nestedClause{Nested: nestedSparse{
		Path: generatedSemanticField + ".chunks", ScoreMode: "max",
		Query: neuralSparseSpec{NeuralSparse: map[string]sparseTokens{
			generatedSemanticField + ".chunks.embedding": {QueryTokens: snapshot.QueryTokens},
		}},
	}})
	if err != nil {
		return nil, queryStepError{operation: "encode sparse clause", err: err}
	}
	body := rankRequestBody{
		Size: size, PIT: rankPIT{ID: snapshot.PITID, KeepAlive: strconv.FormatInt(keepAlive.Milliseconds(), 10) + "ms"},
		Source: []string{"node_id"}, TrackTotalHits: false,
		Query: rankQuery{Bool: rankBool{Filter: filters, Should: []json.RawMessage{lexical, semantic}, MinimumShouldMatch: 1}},
		Sort:  rankSort, SearchAfter: after,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, queryStepError{operation: "encode rank request", err: err}
	}
	return encoded, nil
}

func encodeFilters(query searchdomain.Query) ([]json.RawMessage, error) {
	clauses := make([]json.RawMessage, 0, 4)
	version, err := json.Marshal(termClause{Term: map[string]string{"access.versions": query.Access.Version}})
	if err != nil {
		return nil, queryStepError{operation: "encode access version filter", err: err}
	}
	keys, err := json.Marshal(termsClause{Terms: map[string][]string{"access.keys": query.Access.Keys}})
	if err != nil {
		return nil, queryStepError{operation: "encode access key filter", err: err}
	}
	retired, err := json.Marshal(booleanTermClause{Term: map[string]bool{"retired": false}})
	if err != nil {
		return nil, queryStepError{operation: "encode retirement filter", err: err}
	}
	clauses = append(clauses, version, keys, retired)
	if query.NodeType != "" {
		nodeType, err := json.Marshal(termClause{Term: map[string]string{"node_type": query.NodeType}})
		if err != nil {
			return nil, queryStepError{operation: "encode node type filter", err: err}
		}
		clauses = append(clauses, nodeType)
	}
	return clauses, nil
}

// queryStepError adds operation context to a query step failure. The
// caller logs it once.
type queryStepError struct {
	operation string
	err       error
}

func (e queryStepError) Error() string { return e.operation + ": " + e.err.Error() }
func (e queryStepError) Unwrap() error { return e.err }
