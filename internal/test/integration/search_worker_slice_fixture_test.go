package integration

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
)

const (
	// escapedNameBytes is the name prefix each page document stores.
	escapedNameBytes = 512
	// worstEscapedText is one byte that JSON encoding writes as six bytes.
	worstEscapedText = "<"
	// foundationDBValueLimit is the largest value FoundationDB stores. The node
	// record is one JSON value and bounds the text of one node.
	foundationDBValueLimit = 100_000
	// largestAccessEntries is the version and key count of a page during an
	// access rollout: one key from each of the two registered policies.
	largestAccessEntries = 2
	// setupDeadline bounds the rollout and replacement setup steps.
	setupDeadline = 10 * time.Minute
)

// writeLargestSearchNode rewrites the fixture node with name and one included
// value of repeated unit, as long as the node and view records fit within the
// FoundationDB value limit.
func writeLargestSearchNode(t *testing.T, stores *fdbadapter.Stores, fixture searchFixture, name, unit string) {
	t.Helper()
	now := clock.Now().UTC()
	records := func(included string) (*node.Node, *node.NodeView, int) {
		props := map[string]json.RawMessage{fixture.IncludedKey: mustJSON(included), fixture.ExcludedKey: mustJSON(readerExcludedValue)}
		value := &node.Node{ID: fixture.NodeID, OrgID: fixture.OrgID, NodeType: fixture.TypeKey, Name: name, Props: props, CreatedAt: now, UpdatedAt: now}
		view := &node.NodeView{ID: fixture.NodeID, OrgID: fixture.OrgID, NodeType: fixture.TypeKey, Name: name, Props: props, CreatedAt: now, UpdatedAt: now}
		encodedValue, valueErr := json.Marshal(value)
		encodedView, viewErr := json.Marshal(view)
		if valueErr != nil || viewErr != nil {
			t.Fatalf("encode node records: %v %v", valueErr, viewErr)
		}
		return value, view, max(len(encodedValue), len(encodedView))
	}
	_, _, base := records("")
	_, _, one := records(unit)
	value, view, size := records(strings.Repeat(unit, (foundationDBValueLimit-base)/(one-base)))
	if size > foundationDBValueLimit || size+(one-base) <= foundationDBValueLimit {
		t.Fatalf("largest node record is %d bytes, want the largest size within %d", size, foundationDBValueLimit)
	}
	if err := stores.Nodes.Set(t.Context(), value, view); err != nil {
		t.Fatalf("store the largest search node: %v", err)
	}
}

// driveClasses claims and processes work of the listed classes only, until
// done reports true. Other classes stay pending.
func driveClasses(t *testing.T, worker *service.SearchWorker, work *fdbadapter.SearchWorkStore, classes []searchdomain.WorkClass, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(setupDeadline); time.Now().Before(deadline); {
		if done() {
			return
		}
		claimed := false
		for _, class := range classes {
			item, err := work.Claim(t.Context(), class, "setup", 30*time.Second)
			if errors.Is(err, searchdomain.ErrNoWork) {
				continue
			}
			if err != nil {
				t.Fatalf("claim %s work: %v", class, err)
			}
			claimed = true
			if err := worker.Process(t.Context(), item); err != nil {
				t.Fatalf("process %s work: %v", class, err)
			}
		}
		if !claimed {
			time.Sleep(250 * time.Millisecond)
		}
	}
	t.Fatalf("setup classes %v did not finish within %s", classes, setupDeadline)
}

// sliceBytes counts the bulk writes per index and returns the total and the
// largest request body of one slice.
func sliceBytes(records []bulkRecord) (writes map[string]int, total, largest int) {
	writes = map[string]int{}
	for _, record := range records {
		writes[record.index]++
		total += record.bytes
		largest = max(largest, record.bytes)
	}
	return writes, total, largest
}
