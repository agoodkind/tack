package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	scaleDistinctNodes  = 1501
	scaleDuplicatePages = 36000
	scaleQuery          = "orbital manifest checklist"
	scaleWriteBatch     = 100
	scaleBulkBatch      = 500
	// cursorTiedPages is the number of copies of one corpus page. The copies
	// and the original page share one score and one node ID. The group
	// contains more matches than one raw batch of 100 matches.
	cursorTiedPages = 120
	// cursorTiedOrdinalStart is the first page ordinal of the copies.
	cursorTiedOrdinalStart = 1000
	// cursorTieBatches bounds the raw batches of the tied traversal.
	cursorTieBatches = 20
)

// putTiedPages bulk-indexes count copies of the single indexed page of
// nodeID. Each copy has a new document ID and page ordinal and keeps the
// node ID, type, revision, name, text, generation, and access of the page.
// OpenSearch generates the sparse chunks of each copy from the same text.
func putTiedPages(t *testing.T, fixture queryFixture, nodeID uuid.UUID, count int) {
	t.Helper()
	pages := searchNodePages(t, fixture.Client, fixture.Index, nodeID, false)
	if len(pages) != 1 {
		t.Fatalf("node %s has %d active pages, want 1 to copy", nodeID, len(pages))
	}
	original := pages[0]
	generation, err := strconv.Atoi(original.SearchGeneration.String())
	if err != nil {
		t.Fatalf("parse search generation %q of page %s: %v", original.SearchGeneration, original.ID, err)
	}
	var body strings.Builder
	for ordinal := range count {
		document := nativeDocument{
			NodeID: original.NodeID, NodeType: original.NodeType, NodeRevision: original.NodeRevision,
			ProjectionVersion: original.ProjectionVersion, PageOrdinal: cursorTiedOrdinalStart + ordinal,
			Name: original.Name, PageText: original.PageText, Retired: false,
			SearchGeneration: generation, Access: original.Access,
		}
		id := "tied-" + nodeID.String() + "-" + strconv.Itoa(ordinal)
		body.WriteString(nativeIndexAction(t, fixture.Index, id, 1, encodeNativeJSON(t, document)))
	}
	requireBulkSucceeded(t, nativeBulk(t, fixture.Client, body.String()))
}

// requireTiedPagesOnce reads every raw batch of the session point in time
// from the first match until OpenSearch returns an empty batch. It requires
// each active page document of nodeID to appear exactly once, identified by
// its shard document sort value. It also requires at least one batch
// boundary between two matches with equal score and node ID.
func requireTiedPagesOnce(t *testing.T, fixture queryFixture, ranker *search.QueryRanker, session searchdomain.Session, nodeID uuid.UUID) {
	t.Helper()
	want := len(searchNodePages(t, fixture.Client, fixture.Index, nodeID, false))
	snapshot := session.Snapshot
	var after json.RawMessage
	var previous []json.RawMessage
	positions := make(map[string]bool, want)
	tiedBoundaries := 0
	for range cursorTieBatches {
		batch, err := ranker.Read(WithTestOperation(t, t.Context()), session.Query, snapshot, after)
		if err != nil {
			t.Fatalf("read tied raw batch: %v", err)
		}
		if len(batch.Hits) == 0 {
			if len(positions) != want || tiedBoundaries == 0 {
				t.Fatalf("traversal read %d of %d page documents of node %s with %d tied batch boundaries, want every document and at least one tied boundary",
					len(positions), want, nodeID, tiedBoundaries)
			}
			return
		}
		snapshot.PITID = batch.PITID
		first := hitSortValues(t, batch.Hits[0])
		if previous != nil && bytes.Equal(previous[0], first[0]) && bytes.Equal(previous[1], first[1]) {
			tiedBoundaries++
		}
		for _, hit := range batch.Hits {
			if hit.NodeID != nodeID {
				continue
			}
			position := string(hitSortValues(t, hit)[2])
			if positions[position] {
				t.Fatalf("traversal read the page document of node %s at shard position %s twice", nodeID, position)
			}
			positions[position] = true
		}
		last := batch.Hits[len(batch.Hits)-1]
		previous, after = hitSortValues(t, last), last.Sort
	}
	t.Fatalf("tied traversal did not end within %d raw batches", cursorTieBatches)
}

// hitSortValues returns the score, node ID, and shard document sort values
// of one match as raw JSON.
func hitSortValues(t *testing.T, hit searchdomain.RankHit) []json.RawMessage {
	t.Helper()
	var values []json.RawMessage
	if err := json.Unmarshal(hit.Sort, &values); err != nil || len(values) != 3 {
		t.Fatalf("sort values %s are not score, node ID, and shard document: %v", hit.Sort, err)
	}
	return values
}

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
