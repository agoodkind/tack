package integration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
)

const (
	replacementDeadline = 10 * time.Minute
	replacementPause    = 250 * time.Millisecond
)

// beginReplacement runs ops search reindex --mode full and returns the target
// index from its response. It registers target index deletion for test cleanup.
func beginReplacement(t *testing.T, fixture parentEdgeFixture) string {
	t.Helper()
	result, err := fixture.runOps(t, "ops", "search", "reindex", "--mode", "full")
	if err != nil {
		t.Fatalf("ops search reindex --mode full: %v", err)
	}
	var replacement struct {
		TargetIndex string `json:"target_index"`
	}
	if err := json.Unmarshal(result, &replacement); err != nil || replacement.TargetIndex == "" {
		t.Fatalf("decode ops search reindex result %s: %v", result, err)
	}
	t.Cleanup(func() { _ = fixture.adapter.DeleteIndex(context.WithoutCancel(t.Context()), replacement.TargetIndex) })
	return replacement.TargetIndex
}

// runReplacementUntil runs search worker slices until done returns true for
// the current rebuild and its presence flag. It fails the test if the
// replacement deadline expires first.
func runReplacementUntil(t *testing.T, fixture parentEdgeFixture, done func(searchdomain.Rebuild, bool) bool) {
	t.Helper()
	rebuilds := fixture.stores.SearchRebuilds(clock.Wall{})
	deadline := clock.Now().Add(replacementDeadline)
	for clock.Now().Before(deadline) {
		current, found, err := rebuilds.CurrentRebuild(t.Context())
		if err != nil {
			t.Fatalf("read index replacement: %v", err)
		}
		if done(current, found) {
			return
		}
		runSliceOrWait(t, fixture)
	}
	t.Fatalf("the index replacement stayed short of the expected state for %s", replacementDeadline)
}

func replacementFinished(_ searchdomain.Rebuild, found bool) bool { return !found }

// runSliceOrWait runs one production search worker slice and pauses when the
// worker does not claim work. It fails the test if the slice returns an error.
func runSliceOrWait(t *testing.T, fixture parentEdgeFixture) {
	t.Helper()
	claimed, err := fixture.worker.RunSlice(t.Context())
	if err != nil {
		t.Fatalf("run search worker slice: %v", err)
	}
	if !claimed {
		waitUntilTime(t, clock.Now().Add(replacementPause))
	}
}

// editLeafText updates a node's included text property through NodeService
// as the fixture's first actor. It fails the test if the update fails.
func editLeafText(t *testing.T, fixture parentEdgeFixture, nodeID uuid.UUID, text string) {
	t.Helper()
	_, err := fixture.nodes.Update(t.Context(), service.UpdateInput{
		NodeID: nodeID, Name: nil, Props: map[string]json.RawMessage{fixture.kind.included: parentEdgeJSON(t, text)},
		RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: fixture.first.Actors[0].UserID,
	})
	if err != nil {
		t.Fatalf("edit node %s: %v", nodeID, err)
	}
}

func servingIndex(t *testing.T, fixture parentEdgeFixture) string {
	t.Helper()
	index, err := fixture.stores.ServingSearchIndex(t.Context())
	if err != nil {
		t.Fatalf("read serving search index: %v", err)
	}
	return index
}

// requireReplaced requires the serving index and public alias to select
// target. It also requires a settings lookup for source to return an error.
func requireReplaced(t *testing.T, fixture parentEdgeFixture, target, source string) {
	t.Helper()
	if serving := servingIndex(t, fixture); serving != target {
		t.Fatalf("serving index = %q, want %q", serving, target)
	}
	if alias, err := fixture.adapter.AliasTarget(t.Context(), search.PublicAlias); err != nil || alias != target {
		t.Fatalf("public alias target = %q, error = %v, want %q", alias, err, target)
	}
	if _, err := fixture.adapter.IndexSettings(t.Context(), source); err == nil {
		t.Fatalf("previous index %s still exists", source)
	}
}
