package integration_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// parentEdgePause spaces the worker slices while the replacement waits.
const parentEdgePause = 250 * time.Millisecond

// TestSearchParentEdgeReindexReadmits creates a leaf under another leaf
// through the node service. The relationship store adds a second child_of
// edge from the nested leaf to the second entry point. The search worker must
// exclude the nested leaf, and search must not return it. A relationship
// store without search work removes the second edge without scheduling search
// work. The test requires the audited command ops search reindex --mode full
// to replace the serving index. After the replacement, search must return the
// nested leaf, and ops search verify must not list an exclusion of it.
func TestSearchParentEdgeReindexReadmits(t *testing.T) {
	fixture := newParentEdgeFixture(t)
	parent := fixture.createLeaf(t, fixture.firstEntry, "topaz parent leaf")
	nested := fixture.createLeaf(t, parent, "topaz nested leaf")
	fixture.drain(t)
	second := &node.Relationship{OrgID: fixture.kind.orgID, SourceID: nested, RelationType: node.RelChildOf, TargetID: fixture.secondEntry}
	if err := fixture.stores.Relationships.Add(t.Context(), second); err != nil {
		t.Fatalf("add second parent edge of nested leaf %s: %v", nested, err)
	}
	fixture.drain(t)
	if !slices.Contains(fixture.excluded(t), nested) {
		t.Fatalf("ops search verify does not list the excluded nested leaf %s", nested)
	}
	if found := fixture.search(t, fixture.first, "topaz nested leaf"); slices.Contains(found, nested) {
		t.Fatalf("search returned the excluded nested leaf %s", nested)
	}
	unscheduled, err := fdbadapter.NewStores(fixture.cfg.FDBClusterFile, cohortTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores without search work: %v", err)
	}
	if err := unscheduled.Relationships.Remove(t.Context(), fixture.kind.orgID, nested, node.RelChildOf, fixture.secondEntry); err != nil {
		t.Fatalf("remove second parent edge of nested leaf %s: %v", nested, err)
	}

	fixture.reindex(t)
	fixture.drain(t)

	if found := fixture.search(t, fixture.first, "topaz nested leaf"); !slices.Contains(found, nested) {
		t.Fatalf("search after the reindex returned %v, want nested leaf %s", found, nested)
	}
	if slices.Contains(fixture.excluded(t), nested) {
		t.Fatalf("ops search verify lists nested leaf %s after the reindex", nested)
	}
}

func (f parentEdgeFixture) excluded(t *testing.T) []uuid.UUID {
	t.Helper()
	result, verifyErr := f.runOps(t, "ops", "search", "verify")
	if verifyErr != nil {
		t.Fatalf("ops search verify: %v", verifyErr)
	}
	var listing struct {
		Exclusions []struct {
			NodeID uuid.UUID `json:"node_id"`
		} `json:"exclusions"`
	}
	if err := json.Unmarshal(result, &listing); err != nil {
		t.Fatalf("decode ops search verify listing %s: %v", result, err)
	}
	nodes := make([]uuid.UUID, 0, len(listing.Exclusions))
	for _, exclusion := range listing.Exclusions {
		nodes = append(nodes, exclusion.NodeID)
	}
	return nodes
}

// reindex runs the audited command ops search reindex --mode full, then runs
// search worker slices. It stops when the index replacement record does not
// exist or the replacement is in the retiring state.
// The replacement switches the serving index before it enters the retiring
// state. In that state, it waits until open search sessions stop reading the
// previous index.
func (f parentEdgeFixture) reindex(t *testing.T) {
	t.Helper()
	result, err := f.runOps(t, "ops", "search", "reindex", "--mode", "full")
	if err != nil {
		t.Fatalf("ops search reindex --mode full: %v", err)
	}
	var replacement struct {
		TargetIndex string `json:"target_index"`
	}
	if err := json.Unmarshal(result, &replacement); err != nil || replacement.TargetIndex == "" {
		t.Fatalf("decode ops search reindex result %s: %v", result, err)
	}
	t.Cleanup(func() { _ = f.adapter.DeleteIndex(context.WithoutCancel(t.Context()), replacement.TargetIndex) })
	rebuilds := f.stores.SearchRebuilds(clock.Wall{})
	deadline := clock.Now().Add(parentEdgeDeadline)
	for clock.Now().Before(deadline) {
		current, running, err := rebuilds.CurrentRebuild(t.Context())
		if err != nil {
			t.Fatalf("read index replacement: %v", err)
		}
		if !running || current.State == searchdomain.RebuildRetiring {
			return
		}
		claimed, err := f.worker.RunSlice(t.Context())
		if err != nil {
			t.Fatalf("search slice failed during the replacement: %v", err)
		}
		if !claimed {
			<-time.After(parentEdgePause)
		}
	}
	t.Fatalf("the index replacement did not switch the serving index within %s", parentEdgeDeadline)
}
