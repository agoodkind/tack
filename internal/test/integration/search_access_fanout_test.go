package integration

import (
	"slices"
	"strings"
	"testing"

	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// TestSearchAccessDependentsSurviveEdit requires every grandchild page to
// store the child's new access keys after an edit rewrites the child's access
// work. The test moves the child, stops its access work in the dependents
// phase, and then edits the child.
func TestSearchAccessDependentsSurviveEdit(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, index := newSearchIndex(t, stores)
	source := clock.Wall{}
	moved := newMovableChild(t, stores, strings.Repeat("dependent access page text ", 6))
	grandchild := newGrandchild(t, stores, moved, strings.Repeat("grandchild access page text ", 6))
	worker := newSearchWorker(t, stores, adapter, source, searchWorkerSettings(accessPageBytes))
	runSearchWorkerUntilIdle(t, worker)
	before := searchNodePages(t, client, index, grandchild.NodeID, false)
	if len(before) == 0 {
		t.Fatal("grandchild has no indexed pages")
	}

	moveChild(t, stores, moved)
	store := stores.SearchWork(source)
	if err := worker.Process(t.Context(), claimAccessFor(t, store, moved.Fixture.NodeID)); err != nil {
		t.Fatalf("process child access pages: %v", err)
	}
	pending := claimAccessFor(t, store, moved.Fixture.NodeID)
	if pending.Phase != searchdomain.PhaseDependents {
		t.Fatalf("child access work phase = %q, want the dependents phase", pending.Phase)
	}
	if err := store.Yield(t.Context(), pending); err != nil {
		t.Fatalf("yield child access work: %v", err)
	}
	writeSearchNode(t, stores, moved.Fixture, "edited child text", readerExcludedValue)
	runSearchWorkerUntilIdle(t, worker)

	childPages := searchNodePages(t, client, index, moved.Fixture.NodeID, false)
	after := searchNodePages(t, client, index, grandchild.NodeID, false)
	if len(childPages) == 0 || len(after) != len(before) {
		t.Fatalf("child has %d pages and grandchild %d pages, want pages for both and %d grandchild pages", len(childPages), len(after), len(before))
	}
	for position, page := range after {
		if slices.Equal(page.Access.Keys, before[position].Access.Keys) || !slices.Equal(page.Access.Keys, childPages[0].Access.Keys) {
			t.Fatalf("grandchild page %d access %v, want the child's new access %v", position, page.Access.Keys, childPages[0].Access.Keys)
		}
	}
}

// TestSearchAccessUpdatesOlderActiveRevision requires access work to write
// the new access keys at a higher search generation to every visible page of
// the older revision. The test edits the child, leaves the new revision
// pending, moves the child, and runs only access work.
func TestSearchAccessUpdatesOlderActiveRevision(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, index := newSearchIndex(t, stores)
	source := clock.Wall{}
	moved := newMovableChild(t, stores, strings.Repeat("older revision page text ", 6))
	worker := newSearchWorker(t, stores, adapter, source, searchWorkerSettings(accessPageBytes))
	runSearchWorkerUntilIdle(t, worker)
	before := searchNodePages(t, client, index, moved.Fixture.NodeID, false)
	if len(before) < 2 {
		t.Fatalf("child has %d pages, want several", len(before))
	}

	writeSearchNode(t, stores, moved.Fixture, strings.Repeat("newer revision page text ", 6), readerExcludedValue)
	moveChild(t, stores, moved)
	drainClass(t, stores.SearchWork(source), worker, searchdomain.WorkClassAccess)

	after := searchNodePages(t, client, index, moved.Fixture.NodeID, false)
	if len(after) != len(before) {
		t.Fatalf("visible pages changed from %d to %d before the new revision was indexed", len(before), len(after))
	}
	for position, page := range after {
		previous := before[position]
		if page.NodeRevision != previous.NodeRevision || *page.PageText != *previous.PageText {
			t.Fatalf("page %d is not the older active revision", position)
		}
		if slices.Equal(page.Access.Keys, previous.Access.Keys) || pageGeneration(t, page) <= pageGeneration(t, previous) {
			t.Fatalf("older revision page %d kept access %v at generation %s", position, page.Access.Keys, page.SearchGeneration)
		}
	}
}
