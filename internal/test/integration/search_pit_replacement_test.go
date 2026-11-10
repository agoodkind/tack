package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/test/integration"
)

// replacementKeepAlive is the lifetime of the point in time that the test
// substitutes into a ranked batch response.
const replacementKeepAlive = time.Minute

// continuationRequest decodes the point in time and the search_after JSON of
// one ranked batch request.
type continuationRequest struct {
	PIT         pitReference    `json:"pit"`
	SearchAfter json.RawMessage `json:"search_after"`
}

// sortedHits decodes each match's node ID and raw sort JSON from one ranked
// batch response.
type sortedHits struct {
	Hits struct {
		Hits []struct {
			Source capturedSource  `json:"_source"`
			Sort   json.RawMessage `json:"sort"`
		} `json:"hits"`
	} `json:"hits"`
}

// TestSearchDecoderKeepsReplacementPointInTime creates a second point in time
// on the serving index. The capture proxy puts its ID in place of the pit_id
// of the first ranked batch response that OpenSearch returns for a new
// session over 26 matching nodes. The stored session must keep the
// replacement ID and the exact sort JSON of the last consumed match. Every
// later ranked request must send the replacement ID, and the continuation
// must send that sort JSON byte for byte as search_after. The continuation
// must return the remaining node once, and the completed session must close
// the replacement point in time.
func TestSearchDecoderKeepsReplacementPointInTime(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	workspace := fixture.Workspaces[0]
	entryID := integration.EntryPoint(t, fixture, workspace)
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	nodes := putFullPageCorpus(t, fixture, kind, entryID, fullPageResults+1)
	integration.DrainSearchWork(t, fixture.Worker, fullPageSlicesPerNode*len(nodes)+fullPageFixtureSlices)
	harness, capture := newCaptureHarness(t, fixture, false)
	replacement := createReplacementPIT(t, fixture)
	capture.replaceNextPIT(replacement)

	first := fullPageSearch(t, harness, kind.TypeKey, "")
	if first.Complete || len(first.IDs) != fullPageResults {
		t.Fatalf("the first page returned %d results and completion %t, want %d results and a cursor", len(first.IDs), first.Complete, fullPageResults)
	}
	opened := capture.take()
	t.Cleanup(func() { deleteTestPIT(t, fixture, decodeContinuation(t, opened[0]).PIT.ID) })
	requireContinuationPIT(t, opened[1:], replacement)
	session := integration.CursorSession(t, fixture, first.Cursor)
	if session.Snapshot.PITID != replacement {
		t.Fatalf("the session stores point in time %q, want the replacement %q", session.Snapshot.PITID, replacement)
	}
	requireResponseSort(t, opened, session.Sort, first.IDs[len(first.IDs)-1])

	second := fullPageSearch(t, harness, kind.TypeKey, first.Cursor)
	if !second.Complete || len(second.IDs) != 1 {
		t.Fatalf("the continuation returned %v and completion %t, want one result and completion", second.IDs, second.Complete)
	}
	integration.RequireExactlyOnce(t, slices.Concat(first.IDs, second.IDs), nodes)
	continued := capture.take()
	requireContinuationPIT(t, continued, replacement)
	if after := decodeContinuation(t, continued[0]).SearchAfter; !bytes.Equal(after, session.Sort) {
		t.Fatalf("the continuation sent search_after %s, want the stored sort JSON %s", after, session.Sort)
	}
	requirePITsClosed(t, fixture, []string{replacement})
}

// createReplacementPIT creates a point in time on the serving index and
// returns its ID.
func createReplacementPIT(t *testing.T, fixture integration.QueryFixture) string {
	t.Helper()
	created, err := fixture.Client.PointInTime.Create(integration.WithTestOperation(t, t.Context()), &opensearchapi.CreatePITReq{
		Indices: []string{fixture.Index}, Header: nil, Params: &opensearchapi.CreatePITParams{KeepAlive: replacementKeepAlive},
	})
	if err != nil || !created.PITID.IsSet() {
		t.Fatalf("create the replacement point in time: %v", err)
	}
	return created.PITID.String()
}

// deleteTestPIT deletes the point in time that the session stopped using
// after the replacement. The test ignores the result because the point in
// time can already be closed.
func deleteTestPIT(t *testing.T, fixture integration.QueryFixture, pitID string) {
	t.Helper()
	ctx := context.WithoutCancel(integration.WithTestOperation(t, t.Context()))
	id, err := opensearchapi.ParsePITID(pitID)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fixture.Client.PointInTime.Delete(ctx, &opensearchapi.DeletePITReq{
		Body: &opensearchapi.DeletePITBody{PITID: []opensearchapi.PITID{id}}, Header: nil, Params: &opensearchapi.DeletePITParams{},
	})
}

func decodeContinuation(t *testing.T, search capturedSearch) continuationRequest {
	t.Helper()
	var request continuationRequest
	if err := json.Unmarshal(search.Request, &request); err != nil {
		t.Fatalf("decode ranked search request: %v", err)
	}
	return request
}

// requireContinuationPIT requires every request in searches to send pitID.
func requireContinuationPIT(t *testing.T, searches []capturedSearch, pitID string) {
	t.Helper()
	for number, search := range searches {
		if sent := decodeContinuation(t, search).PIT.ID; sent != pitID {
			t.Fatalf("ranked request %d sent point in time %q, want the replacement %q", number+1, sent, pitID)
		}
	}
}

// requireResponseSort requires one captured response match to contain
// exactly the stored sort JSON and requires that match to belong to nodeID.
func requireResponseSort(t *testing.T, searches []capturedSearch, stored json.RawMessage, nodeID uuid.UUID) {
	t.Helper()
	for _, search := range searches {
		var response sortedHits
		if err := json.Unmarshal(search.Response, &response); err != nil {
			t.Fatalf("decode ranked search response: %v", err)
		}
		for _, hit := range response.Hits.Hits {
			if bytes.Equal(hit.Sort, stored) {
				if hit.Source.NodeID != nodeID {
					t.Fatalf("the stored sort JSON %s belongs to node %s, want the last result %s", stored, hit.Source.NodeID, nodeID)
				}
				return
			}
		}
	}
	t.Fatalf("no captured response match contains the stored sort JSON %s", stored)
}
