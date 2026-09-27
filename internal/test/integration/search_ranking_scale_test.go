package integration

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
)

const (
	scaleDistinctNodes  = 1501
	scaleDuplicatePages = 36000
	scaleQuery          = "orbital manifest checklist"
	scaleWriteBatch     = 100
	scaleBulkBatch      = 500
)

// putDuplicatePages writes count page documents for one node through the
// typed bulk API in bounded batches. Each page stores the node's current
// access, and OpenSearch admits it for the caller.
func putDuplicatePages(t *testing.T, fixture queryFixture, kind opaqueKind, nodeID uuid.UUID, filter searchdomain.AccessFilter, count int) {
	t.Helper()
	for start := 0; start < count; start += scaleBulkBatch {
		var body strings.Builder
		for ordinal := start; ordinal < min(count, start+scaleBulkBatch); ordinal++ {
			text := scaleQuery + " duplicate page " + strconv.Itoa(ordinal)
			document := nativeDocument{
				NodeID: nodeID.String(), NodeType: kind.TypeKey, NodeRevision: "duplicate", ProjectionVersion: "duplicate",
				PageOrdinal: ordinal + 1, Name: "Orbital duplicate", PageText: &text, Retired: false, SearchGeneration: 1,
				Access: nativeAccess{Versions: []string{filter.Version}, Keys: filter.Keys, Generation: 1},
			}
			id := "duplicate-" + nodeID.String() + "-" + strconv.Itoa(ordinal)
			body.WriteString(nativeIndexAction(t, fixture.Index, id, 1, encodeNativeJSON(t, document)))
		}
		requireBulkSucceeded(t, nativeBulk(t, fixture.Client, body.String()))
	}
}

// TestSearchContinuationTraversesDuplicateHeavyCorpus indexes 1,501
// distinct matching nodes plus 36,000 duplicate pages for one node across
// three primary shards. Continuation must return every node exactly once and
// stop only after an empty raw batch.
func TestSearchContinuationTraversesDuplicateHeavyCorpus(t *testing.T) {
	options := defaultQueryOptions()
	options.Primaries = 3
	fixture := newQueryFixture(t, options)
	workspace := fixture.Workspaces[0]
	entryID := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	expected := make([]uuid.UUID, 0, scaleDistinctNodes+1)
	for start := 0; start < scaleDistinctNodes; start += scaleWriteBatch {
		for number := start; number < min(scaleDistinctNodes, start+scaleWriteBatch); number++ {
			expected = append(expected, putOpaqueNode(t, fixture, kind, entryID, fmt.Sprintf("Orbital item %d", number), scaleQuery, "excluded"))
		}
		drainSearchWork(t, fixture.Worker, 1000)
	}
	duplicate := putOpaqueNode(t, fixture, kind, entryID, "Orbital duplicate", scaleQuery, "excluded")
	drainSearchWork(t, fixture.Worker, 100)
	expected = append(expected, duplicate)
	filter, err := fixture.Stores.SearchPolicySet().Query(t.Context(), searchaccess.AccessRequest{
		Version: "", PrincipalID: workspace.Actors[0].UserID, AuthorityID: workspace.OrgID,
		EntryPointID: entryID, MemberOrganizations: []uuid.UUID{workspace.OrgID},
	})
	if err != nil {
		t.Fatalf("compile caller access: %v", err)
	}
	putDuplicatePages(t, fixture, kind, duplicate, filter, scaleDuplicatePages)
	pages := callEverySearchPage(t, scaleQuery, fixture.Harness)
	requireCorpusOnce(t, pages.IDs, expected, entryID)
	minimumContinuations := (scaleDistinctNodes + scaleDuplicatePages) / 400
	if len(pages.Cursors) < minimumContinuations {
		t.Fatalf("traversal used %d continuations, want at least %d for four batches of 100 per response", len(pages.Cursors), minimumContinuations)
	}
}
