package integration

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
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
// access, and the caller's access filter matches it.
func putDuplicatePages(t *testing.T, fixture queryFixture, kind opaqueKind, nodeID uuid.UUID, filter searchdomain.AccessFilter, count int) {
	t.Helper()
	for start := 0; start < count; start += scaleBulkBatch {
		started := clock.Now()
		batch := start/scaleBulkBatch + 1
		documents := min(count, start+scaleBulkBatch) - start
		t.Logf("duplicate.batch.start ordinal=%d documents=%d timestamp=%s", batch, documents, started.UTC().Format(time.RFC3339Nano))
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
		t.Logf("duplicate.batch.complete ordinal=%d documents=%d wall_ms=%d", batch, documents, clock.Since(started).Milliseconds())
	}
}

// TestSearchContinuationTraversesDuplicateHeavyCorpus requires continuation
// to return every node exactly once from 1,501 distinct matching nodes plus
// 36,000 duplicate pages of one node on three primary shards. The traversal
// must use at least one continuation for every 400 indexed pages.
func TestSearchContinuationTraversesDuplicateHeavyCorpus(t *testing.T) {
	started := clock.Now()
	t.Logf("duplicate.phase.start phase=fixture timestamp=%s", started.UTC().Format(time.RFC3339Nano))
	options := defaultQueryOptions()
	options.Primaries = 3
	fixture := newQueryFixture(t, options)
	t.Logf("duplicate.phase.complete phase=fixture wall_ms=%d", clock.Since(started).Milliseconds())
	started = clock.Now()
	t.Logf("duplicate.phase.start phase=nodes timestamp=%s", started.UTC().Format(time.RFC3339Nano))
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
	filter := callerAccess(t, fixture, workspace, entryID)
	t.Logf("duplicate.phase.complete phase=nodes nodes=%d wall_ms=%d", len(expected), clock.Since(started).Milliseconds())
	started = clock.Now()
	t.Logf("duplicate.phase.start phase=pages documents=%d timestamp=%s", scaleDuplicatePages, started.UTC().Format(time.RFC3339Nano))
	putDuplicatePages(t, fixture, kind, duplicate, filter, scaleDuplicatePages)
	t.Logf("duplicate.phase.complete phase=pages documents=%d wall_ms=%d", scaleDuplicatePages, clock.Since(started).Milliseconds())
	started = clock.Now()
	t.Logf("duplicate.phase.start phase=public timestamp=%s", started.UTC().Format(time.RFC3339Nano))
	pages := callEverySearchPage(t, scaleQuery, fixture.Harness)
	t.Logf("duplicate.phase.complete phase=public results=%d cursors=%d wall_ms=%d", len(pages.IDs), len(pages.Cursors), clock.Since(started).Milliseconds())
	requireCorpusOnce(t, pages.IDs, expected, entryID)
	minimumContinuations := (scaleDistinctNodes + scaleDuplicatePages) / 400
	if len(pages.Cursors) < minimumContinuations {
		t.Fatalf("traversal used %d continuations, want at least %d for four batches of 100 per response", len(pages.Cursors), minimumContinuations)
	}
}
