package integration_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	opensearch "github.com/opensearch-project/opensearch-go/v5"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/test/integration"
)

// sparseEmbeddingField selects the chunk embedding field that OpenSearch
// generates for page_text.
const sparseEmbeddingField = "page_text_semantic_info.chunks.embedding"

type profiledQuery struct {
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Children    []profiledQuery `json:"children"`
}

type fieldMappingIndex struct {
	Mappings map[string]fieldMappingEntry `json:"mappings"`
}

type fieldMappingEntry struct {
	Mapping map[string]fieldMappingType `json:"mapping"`
}

type fieldMappingType struct {
	Type string `json:"type"`
}

// requireRankFeaturesField reads the serving index mapping and requires the
// generated embedding field to use rank_features.
func requireRankFeaturesField(t *testing.T, fixture integration.QueryFixture) {
	t.Helper()
	var decoded map[string]fieldMappingIndex
	request := opensearchapi.IndicesGetFieldMappingReq{Indices: []string{fixture.Index}, Fields: []string{sparseEmbeddingField}}
	response, err := opensearch.Execute(t.Context(), fixture.Client.Client, http.MethodGet, request, &decoded)
	if err != nil {
		t.Fatalf("read mapping of %s: %v", sparseEmbeddingField, err)
	}
	if response.IsError() {
		t.Fatalf("read mapping of %s: %v", sparseEmbeddingField, opensearch.ParseError(response))
	}
	for _, mapping := range decoded[fixture.Index].Mappings[sparseEmbeddingField].Mapping {
		if mapping.Type == "rank_features" {
			return
		}
	}
	t.Fatalf("index %s does not map %s as rank_features: %+v", fixture.Index, sparseEmbeddingField, decoded)
}

// requestKeys collects JSON object keys recursively through objects and
// arrays. It skips descendants of query_tokens because those keys are model
// vocabulary tokens.
func requestKeys(value json.RawMessage) []string {
	var object map[string]json.RawMessage
	if json.Unmarshal(value, &object) == nil {
		keys := make([]string, 0, len(object))
		for key, child := range object {
			keys = append(keys, key)
			if key != "query_tokens" {
				keys = append(keys, requestKeys(child)...)
			}
		}
		return keys
	}
	var array []json.RawMessage
	if json.Unmarshal(value, &array) == nil {
		var keys []string
		for _, child := range array {
			keys = append(keys, requestKeys(child)...)
		}
		return keys
	}
	return nil
}

// profiledQueries returns the supplied query operations and every nested
// child operation.
func profiledQueries(queries []profiledQuery) []profiledQuery {
	all := slices.Clone(queries)
	for _, query := range queries {
		all = append(all, profiledQueries(query.Children)...)
	}
	return all
}
