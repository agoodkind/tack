package integration_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// fullPageResults is the default public page size.
	fullPageResults = 25
	// fullPageMaxBatches is the default limit on engine batches per public response.
	fullPageMaxBatches = 4
	// fullPageQuery is the text every corpus node stores in its included search property.
	fullPageQuery = "harbor lantern ledger"
	// fullPageSlicesPerNode bounds content work from creation and metadata rescans, including one retry
	// after a generation change. It also bounds access work from the entry point's dependents phase
	// and metadata rescans; each access item uses pages and dependents slices and can retry once.
	fullPageSlicesPerNode = 10
	// fullPageFixtureSlices bounds work for the bootstrap organizations and workspaces,
	// the entry point's access and dependents slices, and metadata rescans.
	fullPageFixtureSlices = 100
)

// pitRequest decodes pit.id from a ranked batch request body.
type pitRequest struct {
	PIT pitReference `json:"pit"`
}

type pitReference struct {
	ID string `json:"id"`
}

// pitResponse decodes pit_id from a ranked batch response body.
type pitResponse struct {
	PITID string `json:"pit_id"`
}

// putFullPageCorpus creates count nodes of kind under entryID with fullPageQuery as included text.
// It returns their IDs without running the search worker.
func putFullPageCorpus(t *testing.T, fixture integration.QueryFixture, kind integration.OpaqueKind, entryID uuid.UUID, count int) []uuid.UUID {
	t.Helper()
	nodes := make([]uuid.UUID, 0, count)
	for number := range count {
		name := fmt.Sprintf("Full page node %d", number)
		nodes = append(nodes, integration.PutOpaqueNode(t, fixture, kind, entryID, name, fullPageQuery, "excluded"))
	}
	return nodes
}

// fullPageSearch calls tack_search with node_type and fails the test on an error result.
func fullPageSearch(t *testing.T, harness *integration.MCPHarness, nodeType, cursor string) integration.SearchPage {
	t.Helper()
	page, err := integration.TryTypedSearch(harness, fullPageQuery, nodeType, cursor)
	if err != nil {
		t.Fatalf("the search for %q with node type %s failed: %v", fullPageQuery, nodeType, err)
	}
	return page
}

// capturedPITs returns distinct point in time (PIT) IDs from recorded ranked batch requests
// and responses. It fails the test if it cannot find any IDs.
func capturedPITs(t *testing.T, searches []capturedSearch) []string {
	t.Helper()
	var ids []string
	for _, search := range searches {
		var request pitRequest
		if err := json.Unmarshal(search.Request, &request); err != nil {
			t.Fatalf("the test could not decode the ranked search request: %v", err)
		}
		var response pitResponse
		if err := json.Unmarshal(search.Response, &response); err != nil {
			t.Fatalf("the test could not decode the ranked search response: %v", err)
		}
		for _, id := range []string{request.PIT.ID, response.PITID} {
			if id != "" && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		t.Fatal("the captured ranked batches do not contain a point in time ID")
	}
	return ids
}

// openPITs lists the test engine's open PITs through GET /_search/point_in_time/_all.
func openPITs(t *testing.T, fixture integration.QueryFixture) map[string]bool {
	t.Helper()
	listed, err := fixture.Client.PointInTime.GetAll(integration.WithTestOperation(t, t.Context()), nil)
	if err != nil {
		t.Fatalf("the test could not list open points in time: %v", err)
	}
	open := make(map[string]bool, len(listed.PITs))
	for _, pit := range listed.PITs {
		open[pit.PITID.String()] = true
	}
	return open
}

// requirePITsClosed fails the test if the open PIT listing contains any of the given IDs.
func requirePITsClosed(t *testing.T, fixture integration.QueryFixture, ids []string) {
	t.Helper()
	open := openPITs(t, fixture)
	for _, id := range ids {
		if open[id] {
			t.Fatalf("point in time %s is still open after the search completed", id)
		}
	}
}

// requirePITOpen fails the test unless the open PIT listing contains at least one of the given IDs.
// This check confirms that the listing reports an open PIT.
func requirePITOpen(t *testing.T, fixture integration.QueryFixture, ids []string) {
	t.Helper()
	open := openPITs(t, fixture)
	if !slices.ContainsFunc(ids, func(id string) bool { return open[id] }) {
		t.Fatalf("the open PIT listing does not contain any of the IDs %v while the search has a continuation", ids)
	}
}

// TestSearchFullPageClosesPointInTime calls tack_search with node_type for two opaque node types
// under the caller's entry point. A proxy records every ranked batch. For 25 matching nodes,
// the test requires all nodes once on one complete page without a cursor, at most four ranked
// batches, and closure of every recorded PIT. For 26 matching nodes, the first page must return
// 25 results with a cursor while the PIT remains open. The second page must return the remaining
// node once and complete. Reusing the first cursor must replay that page and its completion value.
// After completion, the test requires closure of every recorded PIT.
func TestSearchFullPageClosesPointInTime(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	workspace := fixture.Workspaces[0]
	entryID := integration.EntryPoint(t, fixture, workspace)
	exact := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	extra := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	exactNodes := putFullPageCorpus(t, fixture, exact, entryID, fullPageResults)
	extraNodes := putFullPageCorpus(t, fixture, extra, entryID, fullPageResults+1)
	integration.DrainSearchWork(t, fixture.Worker, fullPageSlicesPerNode*(len(exactNodes)+len(extraNodes))+fullPageFixtureSlices)
	harness, capture := newCaptureHarness(t, fixture, false)

	only := fullPageSearch(t, harness, exact.TypeKey, "")
	if !only.Complete || only.Cursor != "" {
		t.Fatalf("the search with %d matches returned cursor %q and reported completion as %t; the test requires completion without a cursor", fullPageResults, only.Cursor, only.Complete)
	}
	integration.RequireExactlyOnce(t, only.IDs, exactNodes)
	searches := capture.take()
	if len(searches) > fullPageMaxBatches {
		t.Fatalf("the search with %d matches sent %d ranked batches; the test allows at most %d batches", fullPageResults, len(searches), fullPageMaxBatches)
	}
	requirePITsClosed(t, fixture, capturedPITs(t, searches))

	first := fullPageSearch(t, harness, extra.TypeKey, "")
	if first.Complete || len(first.IDs) != fullPageResults {
		t.Fatalf("the first page of the search with %d matches returned %d results and reported completion as %t; the test requires %d results and a cursor", fullPageResults+1, len(first.IDs), first.Complete, fullPageResults)
	}
	firstPITs := capturedPITs(t, capture.take())
	requirePITOpen(t, fixture, firstPITs)
	second := fullPageSearch(t, harness, extra.TypeKey, first.Cursor)
	if !second.Complete || len(second.IDs) != 1 {
		t.Fatalf("the second page of the search with %d matches returned %v and reported completion as %t; the test requires one result and completion", fullPageResults+1, second.IDs, second.Complete)
	}
	integration.RequireExactlyOnce(t, slices.Concat(first.IDs, second.IDs), extraNodes)
	replay := fullPageSearch(t, harness, extra.TypeKey, first.Cursor)
	if !slices.Equal(replay.IDs, second.IDs) || replay.Complete != second.Complete {
		t.Fatalf("the replay returned %v and reported completion as %t; the test requires %v and completion %t", replay.IDs, replay.Complete, second.IDs, second.Complete)
	}
	requirePITsClosed(t, fixture, slices.Concat(firstPITs, capturedPITs(t, capture.take())))
}
