package integration_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"goodkind.io/tack/internal/test/integration"
)

const (
	// profileQuery supplies the public search text for engine profiling.
	profileQuery = "cobalt relay handbook"
	// sparseQueryType identifies the Lucene operation that evaluates sparse
	// feature weights.
	sparseQueryType = "FeatureQuery"
)

// forbiddenRequestKeys returns the request keys that the profile assertion
// rejects for scripts, nearest-neighbor search, hybrid queries, result
// windows, and total-result caps.
func forbiddenRequestKeys() []string {
	return []string{
		"knn", "k", "neural", "hybrid", "script", "script_score", "function_score",
		"rescore", "window_size", "pagination_depth", "terminate_after",
	}
}

// forbiddenQueryTypes returns lowercase query type fragments that the
// profile assertion rejects.
func forbiddenQueryTypes() []string {
	return []string{"script", "knn", "vector", "hybrid", "functionscore"}
}

type profiledResponse struct {
	Hits    capturedHitList `json:"hits"`
	Profile profileResult   `json:"profile"`
}

type profileResult struct {
	Shards []profileShard `json:"shards"`
}

type profileShard struct {
	Searches []profileSearch `json:"searches"`
}

type profileSearch struct {
	Query []profiledQuery `json:"query"`
}

// requireSparseProfile requires FeatureQuery operations over the embedding
// field and rejects forbidden request keys and profiled query types.
// It requires track_total_hits to be false and the response to omit
// hits.total.
func requireSparseProfile(t *testing.T, search capturedSearch) {
	t.Helper()
	request := decodeCapturedRequest(t, search)
	if string(request.TrackTotalHits) != "false" {
		t.Fatalf("ranked batch sets track_total_hits to %s, want false", request.TrackTotalHits)
	}
	for _, key := range requestKeys(search.Request) {
		if slices.Contains(forbiddenRequestKeys(), key) {
			t.Fatalf("ranked batch sends the forbidden key %q: %s", key, search.Request)
		}
	}
	var response profiledResponse
	if err := json.Unmarshal(search.Response, &response); err != nil {
		t.Fatalf("decode profiled response: %v", err)
	}
	if len(response.Hits.Total) != 0 {
		t.Fatalf("ranked batch response counts total hits: %s", response.Hits.Total)
	}
	if len(response.Profile.Shards) == 0 {
		t.Fatalf("ranked batch response has no profile: %s", search.Response)
	}
	features := 0
	for _, shard := range response.Profile.Shards {
		for _, profiled := range shard.Searches {
			for _, query := range profiledQueries(profiled.Query) {
				lowered := strings.ToLower(query.Type)
				for _, forbidden := range forbiddenQueryTypes() {
					if strings.Contains(lowered, forbidden) {
						t.Fatalf("profile contains the forbidden query type %s: %s", query.Type, query.Description)
					}
				}
				// OpenSearch profiles boosted sparse clauses as BoostQuery operations.
				// The check ignores leading parentheses and counts operations with
				// descriptions that start with FeatureQuery(.
				if !strings.HasPrefix(strings.TrimLeft(query.Description, "("), sparseQueryType+"(") {
					continue
				}
				if !strings.Contains(query.Description, sparseEmbeddingField) {
					t.Fatalf("%s does not read %s: %s", sparseQueryType, sparseEmbeddingField, query.Description)
				}
				features++
			}
		}
	}
	if features == 0 {
		t.Fatalf("profile has no %s over %s: %s", sparseQueryType, sparseEmbeddingField, search.Response)
	}
}

// TestSearchQueryProfileUsesSparseFeatures checks the serving field mapping
// and every ranked batch from public tack_search pages. It requires a
// rank_features field and sparse feature execution without forbidden query
// operations or total hit counts.
func TestSearchQueryProfileUsesSparseFeatures(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	workspace := fixture.Workspaces[0]
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	entryID := integration.EntryPoint(t, fixture, workspace)
	target := integration.PutOpaqueNode(t, fixture, kind, entryID, "Cobalt relay handbook", "cobalt relay handbook steps", "excluded")
	integration.DrainSearchWork(t, fixture.Worker, 500)
	requireRankFeaturesField(t, fixture)
	harness, capture := newCaptureHarness(t, fixture, true)
	ids := integration.CallEverySearchPage(t, profileQuery, harness)
	if !slices.Contains(ids, target) {
		t.Fatalf("search %q did not return node %s: %v", profileQuery, target, ids)
	}
	searches := capture.take()
	if len(searches) == 0 {
		t.Fatalf("search %q sent no ranked batch", profileQuery)
	}
	for _, search := range searches {
		requireSparseProfile(t, search)
	}
}
