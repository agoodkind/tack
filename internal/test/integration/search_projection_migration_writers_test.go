package integration_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	// migrationScanNodes requires two node scan pages of 100 nodes.
	migrationScanNodes     = 121
	migrationScanPageBytes = 256
)

// claimLive claims every available live work item and returns the items
// by node.
func claimLive(t *testing.T, store *fdbadapter.SearchWorkStore) map[uuid.UUID]searchdomain.Work {
	t.Helper()
	claimed := map[uuid.UUID]searchdomain.Work{}
	for {
		work, err := store.Claim(t.Context(), searchdomain.WorkClassLive, "delayed-writer", time.Minute)
		if errors.Is(err, searchdomain.ErrNoWork) {
			return claimed
		}
		if err != nil {
			t.Fatalf("claim live work: %v", err)
		}
		claimed[work.NodeID] = work
	}
}

// requireNewRevisions requires every node to have an active revision later
// than its revision in before. It returns the active revisions.
func (env *migrationEnv) requireNewRevisions(t *testing.T, nodes []uuid.UUID, before map[uuid.UUID]int64, step string) map[uuid.UUID]int64 {
	t.Helper()
	after := env.revisions(t, nodes)
	for nodeID, revision := range after {
		if revision <= before[nodeID] {
			t.Fatalf("node %s kept revision %d after %s", nodeID, revision, step)
		}
	}
	return after
}

// TestSearchProjectionMigrationDominatesNamedRescan checks that activation
// during a named rescan reindexes every node, including nodes the scan
// already read. The named rescan alone does not select any node. A named
// rescan request during the activation's node pass must keep the cursor
// and the selection of every node.
func TestSearchProjectionMigrationDominatesNamedRescan(t *testing.T) {
	env := newMigrationEnv(t, migrationScanPageBytes)
	nodes := env.putNodes(t, migrationScanNodes, "dominance text")
	env.runIdle(t)
	initial := env.revisions(t, nodes)
	inNodePass := func(work searchdomain.Work) bool { return work.Phase == searchdomain.PhasePages && work.Cursor != "" }

	env.putDefinition(t, uniqueKey("p"), true)
	env.driveRescan(t, inNodePass, func(int) {})
	env.forgetProjectionState(t)
	env.runIdle(t)
	migrated := env.requireNewRevisions(t, nodes, initial, "an activation during a named rescan")

	env.forgetProjectionState(t)
	position, _ := env.driveRescan(t, inNodePass, func(int) {})
	env.putDefinition(t, uniqueKey("p"), true)
	resumed, _ := env.driveRescan(t, func(searchdomain.Work) bool { return true }, func(int) {})
	if resumed.Cursor != position.Cursor {
		t.Fatalf("rescan after a named request has cursor %q, want the kept cursor %q", resumed.Cursor, position.Cursor)
	}
	env.runIdle(t)
	env.requireNewRevisions(t, nodes, migrated, "a named request during an activation")
}

// TestSearchProjectionMigrationRejectsDelayedWriters checks that Register
// returns ErrContentChanged for a page read before activation.
// CompleteRefresh must schedule a new content revision when activation
// follows the page writes. The node's live work must remain claimable
// before the activation's node pass schedules it. Every node must then
// use the new projection.
func TestSearchProjectionMigrationRejectsDelayedWriters(t *testing.T) {
	env := newMigrationEnv(t, migrationPageBytes)
	nodes := env.putNodes(t, migrationNodeCount, migrationText)
	env.runIdle(t)
	initial := env.revisions(t, nodes)
	initialProjection := env.pages(t, nodes[0], false)[0].ProjectionVersion

	registering, refreshing := nodes[1], nodes[2]
	env.putNode(t, registering, migrationText+"registering")
	env.putNode(t, refreshing, migrationText+"refreshing")
	store := env.Stores.SearchWork(clock.Wall{})
	claimed := claimLive(t, store)
	registerWork, refreshWork := claimed[registering], claimed[refreshing]
	if registerWork.NodeID != registering || refreshWork.NodeID != refreshing {
		t.Fatalf("claimed live work = %+v, want the work of both edited nodes", claimed)
	}
	delayedPage := env.readerPages(t, registering)[0]
	for _, page := range env.readerPages(t, refreshing) {
		intent, err := store.Register(t.Context(), refreshWork, page)
		if err != nil {
			t.Fatalf("register page %d before the activation: %v", page.Ordinal, err)
		}
		if _, err := env.Adapter.Put(t.Context(), intent); err != nil {
			t.Fatalf("write page %d before the activation: %v", page.Ordinal, err)
		}
		if err := store.CompletePage(t.Context(), intent, page.NextCursor, page.Done); err != nil {
			t.Fatalf("checkpoint page %d before the activation: %v", page.Ordinal, err)
		}
	}

	env.forgetProjectionState(t)
	env.finishMetadataPass(t)
	if _, err := store.Register(t.Context(), registerWork, delayedPage); !errors.Is(err, node.ErrContentChanged) {
		t.Fatalf("register a page read before the activation = %v, want ErrContentChanged", err)
	}
	if err := store.CompleteRefresh(t.Context(), refreshWork); err != nil {
		t.Fatalf("complete refresh after the activation: %v", err)
	}
	restarted, exists := claimLive(t, store)[refreshing]
	if !exists || restarted.Generation <= refreshWork.Generation || restarted.Revision == refreshWork.Revision {
		t.Fatalf("work after a delayed refresh = %+v, want a new generation and revision after %+v", restarted, refreshWork)
	}
	if err := store.Yield(t.Context(), restarted); err != nil {
		t.Fatalf("yield restarted work: %v", err)
	}
	env.runIdle(t)
	env.requireMigrated(t, nodes, initial, initialProjection)
}
