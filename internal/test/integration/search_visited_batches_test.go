package integration_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// visitedDuplicatePages sets the additional page count for the node that
	// the first public page returns.
	visitedDuplicatePages = 900
	// visitedBatchSize sets the expected page matches per ranked batch to the
	// production default of 100.
	visitedBatchSize = 100
	// visitedMaxBatches sets the expected ranked batches per public response to
	// the production default of four.
	visitedMaxBatches = 4
	// visitedRemainingNodes sets the number of distinct nodes that later public
	// pages must return.
	visitedRemainingNodes = 3
	// visitedRemainingText gives the remaining nodes one matching query term
	// and lower scores than the node with duplicate pages.
	visitedRemainingText = "kitchen supply checklist for the spring inventory"
	// visitedLaterPages limits continuation attempts after the empty page.
	visitedLaterPages = 10
)

// requireBatchesOfNode checks that a public response requested four ranked
// batches and that each batch returned 100 page matches from nodeID only.
func requireBatchesOfNode(t *testing.T, searches []capturedSearch, nodeID uuid.UUID, response string) {
	t.Helper()
	if len(searches) != visitedMaxBatches {
		t.Fatalf("the %s response sent %d ranked batches, want %d", response, len(searches), visitedMaxBatches)
	}
	for number, search := range searches {
		hits := decodeCapturedHits(t, search).Hits
		if len(hits) != visitedBatchSize {
			t.Fatalf("batch %d of the %s response returned %d page matches, want %d", number+1, response, len(hits), visitedBatchSize)
		}
		for _, hit := range hits {
			if hit.Source.NodeID != nodeID {
				t.Fatalf("batch %d of the %s response returned a page of node %s, want only node %s", number+1, response, hit.Source.NodeID, nodeID)
			}
		}
	}
}

// TestSearchVisitedBatchesReturnEmptyPage checks continuation across 901
// pages of one node. The test records OpenSearch's ranked batches through a
// proxy. For the second public page, OpenSearch returns four ranked batches
// of 100 page matches each. Every match is a page of the node that the first
// public page returned. The second public page must return zero results
// with a cursor. Later public pages must return each of the three other
// nodes exactly once and complete within 10 pages.
func TestSearchVisitedBatchesReturnEmptyPage(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	workspace := fixture.Workspaces[0]
	entryID := integration.EntryPoint(t, fixture, workspace)
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	visited := integration.PutOpaqueNode(t, fixture, kind, entryID, "Orbital duplicate", integration.ScaleQuery, "excluded")
	remaining := make([]uuid.UUID, 0, visitedRemainingNodes)
	for number := range visitedRemainingNodes {
		name := fmt.Sprintf("Kitchen item %d", number)
		remaining = append(remaining, integration.PutOpaqueNode(t, fixture, kind, entryID, name, visitedRemainingText, "excluded"))
	}
	integration.DrainSearchWork(t, fixture.Worker, 500)
	filter := integration.CallerAccess(t, fixture, workspace, entryID)
	integration.PutDuplicatePages(t, fixture, kind, visited, filter, visitedDuplicatePages)
	harness, capture := newCaptureHarness(t, fixture, false)

	first := integration.CallSearch(t, harness, integration.ScaleQuery, "")
	if !slices.Equal(first.IDs, []uuid.UUID{visited}) || first.Cursor == "" {
		t.Fatalf("the first page returned %v with cursor %q, want only node %s and a cursor", first.IDs, first.Cursor, visited)
	}
	requireBatchesOfNode(t, capture.take(), visited, "first")
	empty := integration.CallSearch(t, harness, integration.ScaleQuery, first.Cursor)
	if len(empty.IDs) != 0 || empty.Cursor == "" {
		t.Fatalf("the second page returned %v with cursor %q, want zero results and a cursor", empty.IDs, empty.Cursor)
	}
	requireBatchesOfNode(t, capture.take(), visited, "second")
	var later []uuid.UUID
	cursor := empty.Cursor
	for range visitedLaterPages {
		page := integration.CallSearch(t, harness, integration.ScaleQuery, cursor)
		later = append(later, page.IDs...)
		if page.Complete {
			integration.RequireCorpusOnce(t, later, remaining, entryID)
			return
		}
		cursor = page.Cursor
	}
	t.Fatalf("search %q did not complete within %d pages after the empty page", integration.ScaleQuery, visitedLaterPages)
}
