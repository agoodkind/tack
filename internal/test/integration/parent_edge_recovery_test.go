package integration_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
)

// parentEdgeExclusionReason is the reason that the search worker recorded
// for a leaf under another leaf under the parent rule that read type pairs.
const parentEdgeExclusionReason = "resolve hierarchy parent: node requires exactly one hierarchy parent"

// parentEdgePause spaces the worker slices while the replacement waits.
const parentEdgePause = 250 * time.Millisecond

// TestSearchParentEdgeReindexReadmits creates a leaf under another
// leaf and excludes the nested leaf through the search work store. The
// previous parent rule left that state. The audited command
// "ops search reindex --mode full" must replace the serving index. After the
// replacement, search must return the nested leaf, and ops search verify
// must list no exclusion of it.
func TestSearchParentEdgeReindexReadmits(t *testing.T) {
	fixture := newParentEdgeFixture(t)
	parent := fixture.createLeaf(t, fixture.firstEntry, "topaz parent leaf")
	nested := fixture.createLeaf(t, parent, "topaz nested leaf")
	fixture.drain(t)
	renamed := "renamed topaz nested leaf"
	if _, err := fixture.nodes.Update(t.Context(), service.UpdateInput{NodeID: nested, Name: &renamed, ActorID: fixture.first.Actors[0].UserID}); err != nil {
		t.Fatalf("rename nested leaf %s: %v", nested, err)
	}
	work := fixture.stores.SearchWork(clock.Wall{})
	claimed, err := work.Claim(t.Context(), searchdomain.WorkClassLive, "parent-edge-test", time.Minute)
	if err != nil || claimed.NodeID != nested {
		t.Fatalf("claim live work = node %s, err %v, want nested leaf %s", claimed.NodeID, err, nested)
	}
	if err := work.Exclude(t.Context(), claimed, parentEdgeExclusionReason); err != nil {
		t.Fatalf("exclude nested leaf %s: %v", nested, err)
	}
	fixture.drain(t)
	if !slices.Contains(fixture.excluded(t), nested) {
		t.Fatalf("ops search verify does not list the excluded nested leaf %s", nested)
	}
	if found := fixture.search(t, fixture.first, "topaz nested leaf"); slices.Contains(found, nested) {
		t.Fatalf("search returned the excluded nested leaf %s", nested)
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

// excluded returns the node IDs that ops search verify lists as excluded.
// The physical checks that follow the listing compare the serving index of
// the fixture with the provisioned index, and their error does not change
// the listing.
func (f parentEdgeFixture) excluded(t *testing.T) []uuid.UUID {
	t.Helper()
	result, verifyErr := f.runOps(t, "ops", "search", "verify")
	t.Logf("ops search verify physical checks: %v", verifyErr)
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

// reindex runs "ops search reindex --mode full" and runs worker slices until
// the replacement finishes.
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
		_, running, err := rebuilds.CurrentRebuild(t.Context())
		if err != nil {
			t.Fatalf("read index replacement: %v", err)
		}
		if !running {
			return
		}
		claimed, err := f.worker.RunSlice(t.Context())
		if err != nil {
			t.Logf("search slice failed during the replacement: %v", err)
		}
		if !claimed {
			<-time.After(parentEdgePause)
		}
	}
	t.Fatalf("the index replacement did not finish within %s", parentEdgeDeadline)
}
