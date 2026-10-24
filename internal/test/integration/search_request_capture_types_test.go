package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// capturedRequest decodes the query and total hit setting from a ranked
// batch request.
type capturedRequest struct {
	// TrackTotalHits preserves the JSON value of track_total_hits for an exact
	// comparison with false.
	TrackTotalHits json.RawMessage `json:"track_total_hits"`
	Query          capturedQuery   `json:"query"`
}

type capturedQuery struct {
	Bool capturedBool `json:"bool"`
}

type capturedBool struct {
	Filter []json.RawMessage `json:"filter"`
	Should []json.RawMessage `json:"should"`
}

// capturedHits decodes the hits object from a ranked batch response.
type capturedHits struct {
	Hits capturedHitList `json:"hits"`
}

type capturedHitList struct {
	// Total preserves hits.total as JSON and remains empty when the response
	// omits that field.
	Total json.RawMessage `json:"total"`
	Hits  []capturedHit   `json:"hits"`
}

type capturedHit struct {
	Source capturedSource `json:"_source"`
}

type capturedSource struct {
	NodeID uuid.UUID `json:"node_id"`
}

func decodeCapturedRequest(t *testing.T, search capturedSearch) capturedRequest {
	t.Helper()
	var request capturedRequest
	if err := json.Unmarshal(search.Request, &request); err != nil {
		t.Fatalf("decode ranked search request %s: %v", search.Request, err)
	}
	return request
}

func decodeCapturedHits(t *testing.T, search capturedSearch) capturedHitList {
	t.Helper()
	var response capturedHits
	if err := json.Unmarshal(search.Response, &response); err != nil {
		t.Fatalf("decode ranked search response: %v", err)
	}
	return response.Hits
}
