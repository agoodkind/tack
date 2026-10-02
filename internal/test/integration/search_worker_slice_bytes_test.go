package integration

import (
	"encoding/json"
	"strings"
	"testing"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
)

const (
	// escapedNameBytes exceeds the 512-byte name the page documents store.
	escapedNameBytes = 600
	// escapedValueBytes gives more than 32 full pages at the 4096-byte limit.
	escapedValueBytes = 110_000
	// worstEscapedText is one byte that JSON encoding writes as six bytes.
	worstEscapedText = "<"
)

// writeEscapedSearchNode rewrites the fixture node with a name and an
// included value made only of a character that JSON encoding escapes.
func writeEscapedSearchNode(t *testing.T, stores *fdbadapter.Stores, fixture searchFixture) {
	t.Helper()
	name := strings.Repeat(worstEscapedText, escapedNameBytes)
	props := map[string]json.RawMessage{
		fixture.IncludedKey: mustJSON(strings.Repeat(worstEscapedText, escapedValueBytes)),
		fixture.ExcludedKey: mustJSON(readerExcludedValue),
	}
	now := clock.Now().UTC()
	value := &node.Node{ID: fixture.NodeID, OrgID: fixture.OrgID, NodeType: fixture.TypeKey, Name: name, Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: fixture.NodeID, OrgID: fixture.OrgID, NodeType: fixture.TypeKey, Name: name, Props: props, CreatedAt: now, UpdatedAt: now}
	if err := stores.Nodes.Set(t.Context(), value, view); err != nil {
		t.Fatalf("store escaped search node: %v", err)
	}
}

// TestSearchWorkerLargestSliceStaysWithinByteMaximum measures the bulk bodies
// of one slice under the production page and byte limits, with full pages of
// text that JSON encoding escapes six to one and a name above the stored
// name limit. The slice must write 32 pages and send less than the 5 MiB
// byte maximum. The slice time and lease are longer than production; the
// page limit then stops the slice while the model encodes each page.
func TestSearchWorkerLargestSliceStaysWithinByteMaximum(t *testing.T) {
	production, err := config.LoadSearchWorkerSettings(t.Context())
	if err != nil {
		t.Fatalf("load production search worker settings: %v", err)
	}
	if production.PageBytes != 4096 || production.MaxPages != boundSlicePages || production.MaxBytes != 5<<20 {
		t.Fatalf("production settings = %d page bytes, %d pages, %d bytes; want 4096, %d, and %d", production.PageBytes, production.MaxPages, production.MaxBytes, boundSlicePages, 5<<20)
	}
	stores := newSearchStore(t)
	adapter, _, meter, _ := newMeteredSearchIndex(t, stores)
	fixture := putSearchText(t, stores, "escaped slice placeholder", readerExcludedValue)
	writeEscapedSearchNode(t, stores, fixture)
	worker := newSearchWorker(t, stores, adapter, clock.Wall{}, boundSettings(production))
	runFirstClaimedSlice(t, worker)
	bodies := meter.take()
	total, largest := 0, 0
	for _, body := range bodies {
		total += body
		largest = max(largest, body)
	}
	t.Logf("largest slice: %d page writes, %d encoded bytes, largest page request %d bytes", len(bodies), total, largest)
	if len(bodies) != production.MaxPages {
		t.Fatalf("slice sent %d page writes, want the page limit %d", len(bodies), production.MaxPages)
	}
	if total >= production.MaxBytes {
		t.Fatalf("slice sent %d encoded bytes, want less than the byte maximum %d", total, production.MaxBytes)
	}
}
