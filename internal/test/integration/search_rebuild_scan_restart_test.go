package integration_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// scanRestartNodes is the number of nodes written for the scan restart test.
	scanRestartNodes = 250
	// scanRestartStepsAfterStop is the expected number of copy scheduling steps
	// needed to finish the scan after the first step.
	scanRestartStepsAfterStop = 2
	scanRestartStoppedLease   = 5 * time.Second
	scanRestartLease          = 5 * time.Minute
)

// TestSearchRebuildScanCursorSurvivesReopenedStores stops a rebuild after its
// first scan step without yielding the claim. After the claim expires and the
// stores reopen, a new SearchWorker finishes the scan from the saved cursor.
// The test checks that the stopped owner cannot schedule copies after the lease
// expires or after a new owner claims the work. It checks that the copy queue
// returns one claim for each written node.
func TestSearchRebuildScanCursorSurvivesReopenedStores(t *testing.T) {
	stores := newIdleClaimStores(t)
	newIdleClaimWorkStore(t, stores)
	nodes := writeScanRestartNodes(t, stores)
	stoppedWork, stopped := stopRebuildAfterFirstScanStep(t, stores)
	waitUntilTime(t, stoppedWork.LeaseUntil)

	reopened := reopenScanRestartStores(t)
	rebuilds := reopened.SearchRebuilds(clock.Wall{})
	current := currentIdleClaimRebuild(t, rebuilds)
	if current.ID != stopped.ID || current.ScanCursor != stopped.ScanCursor || current.ScanComplete {
		t.Fatalf("rebuild on reopened stores = %+v, want rebuild %s at cursor %q", current, stopped.ID, stopped.ScanCursor)
	}
	if pending, err := rebuilds.CopiesPending(integration.WithTestOperation(t, t.Context())); err != nil || !pending {
		t.Fatalf("copies pending on reopened stores = %t, error %v, want the first scan step's copies", pending, err)
	}
	requireStoppedOwnerRefused(t, stores, stoppedWork, stopped)
	if steps := finishScanRestart(t, reopened, stores, stoppedWork, stopped); steps != scanRestartStepsAfterStop {
		t.Fatalf("the scan took %d steps after the stop, want %d from the saved cursor", steps, scanRestartStepsAfterStop)
	}
	requireEveryNodeCopiedOnce(t, reopened, current.TargetIndex, nodes)
}

// writeScanRestartNodes writes one node through stores to schedule search work
// and create the definition and type records required by copy claims. It then
// writes scanRestartNodes nodes without scheduling search work and returns only
// their IDs.
func writeScanRestartNodes(t *testing.T, stores *fdbadapter.Stores) map[uuid.UUID]bool {
	t.Helper()
	unscheduled, err := fdbadapter.NewStores(testenv.FoundationDB(t), idleClaimTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores without search work: %v", err)
	}
	source := putIdleClaimNode(t, stores, uuid.Must(uuid.NewV7()))
	nodes := make(map[uuid.UUID]bool, scanRestartNodes)
	for range scanRestartNodes {
		written := source
		written.NodeID = uuid.Must(uuid.NewV7())
		writeIdleClaimNode(t, unscheduled, written)
		nodes[written.NodeID] = true
	}
	return nodes
}

// stopRebuildAfterFirstScanStep starts and claims a rebuild, advances it to
// copying, and schedules the first page of copies. It returns the work claim
// and the rebuild with its saved cursor.
func stopRebuildAfterFirstScanStep(t *testing.T, stores *fdbadapter.Stores) (searchdomain.Work, searchdomain.Rebuild) {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	if _, err := rebuilds.BeginRebuild(ctx, searchdomain.BeginRebuild{
		Mode: searchdomain.ReplacementFull, PrimaryShards: 1, RoutingShards: idleClaimRoutingShards, Replicas: 0, Restored: false, Reason: "scan restart test",
	}); err != nil {
		t.Fatalf("begin index replacement: %v", err)
	}
	work, claimed, err := stores.SearchWork(clock.Wall{}).Claim(ctx, searchdomain.WorkClassRebuild, "stopped-owner", scanRestartStoppedLease)
	if err != nil || !claimed {
		t.Fatalf("claim replacement work: claimed %t, error %v", claimed, err)
	}
	creating := currentIdleClaimRebuild(t, rebuilds)
	copying := creating
	copying.State = searchdomain.RebuildCopying
	if err := rebuilds.AdvanceRebuild(ctx, work, creating, copying); err != nil {
		t.Fatalf("advance replacement to copying: %v", err)
	}
	if err := rebuilds.CopyNextNodes(ctx, work, currentIdleClaimRebuild(t, rebuilds)); err != nil {
		t.Fatalf("run the first copy scheduling step: %v", err)
	}
	stopped := currentIdleClaimRebuild(t, rebuilds)
	if stopped.ScanCursor == "" || stopped.ScanComplete {
		t.Fatalf("rebuild after one scan step = %+v, want a saved cursor and an unfinished scan", stopped)
	}
	return work, stopped
}

func reopenScanRestartStores(t *testing.T) *fdbadapter.Stores {
	t.Helper()
	reopened, err := fdbadapter.NewStores(testenv.FoundationDB(t), idleClaimTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("reopen stores: %v", err)
	}
	reopened.EnableSearchWork()
	return reopened
}

// finishScanRestart claims rebuild work and runs SearchWorker.Process until
// the scan completes. After the first claim, it checks that the stopped owner
// cannot schedule copies. It returns the number of scan steps.
func finishScanRestart(t *testing.T, stores, stoppedStores *fdbadapter.Stores, stoppedWork searchdomain.Work, stopped searchdomain.Rebuild) int {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	worker := newScanRestartWorker(t, stores)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	steps := 0
	for !currentIdleClaimRebuild(t, rebuilds).ScanComplete {
		if steps > scanRestartStepsAfterStop {
			t.Fatalf("the scan did not finish within %d steps after the stop", steps)
		}
		work, claimed, err := stores.SearchWork(clock.Wall{}).Claim(ctx, searchdomain.WorkClassRebuild, "restarted-owner", scanRestartLease)
		if err != nil || !claimed {
			t.Fatalf("claim replacement work on reopened stores: claimed %t, error %v", claimed, err)
		}
		if steps == 0 {
			requireStoppedOwnerRefused(t, stoppedStores, stoppedWork, stopped)
		}
		if err := worker.Process(ctx, work); err != nil {
			t.Fatalf("run copy scheduling step %d after the stop: %v", steps+1, err)
		}
		steps++
	}
	return steps
}

// requireStoppedOwnerRefused checks that the stopped owner's copy scheduling
// attempt returns searchdomain.ErrWorkChanged.
func requireStoppedOwnerRefused(t *testing.T, stores *fdbadapter.Stores, work searchdomain.Work, stopped searchdomain.Rebuild) {
	t.Helper()
	err := stores.SearchRebuilds(clock.Wall{}).CopyNextNodes(integration.WithTestOperation(t, t.Context()), work, stopped)
	if !errors.Is(err, searchdomain.ErrWorkChanged) {
		t.Fatalf("copy scheduling by the stopped owner returned %v, want %v", err, searchdomain.ErrWorkChanged)
	}
}

// requireEveryNodeCopiedOnce claims available copy work and checks that each
// written node has one claim for the expected target.
func requireEveryNodeCopiedOnce(t *testing.T, stores *fdbadapter.Stores, target string, nodes map[uuid.UUID]bool) {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	work := stores.SearchWork(clock.Wall{})
	copied := make(map[uuid.UUID]bool, len(nodes))
	for {
		item, claimed, err := work.Claim(ctx, searchdomain.WorkClassCopy, "restarted-owner", scanRestartLease)
		if err != nil {
			t.Fatalf("claim a replacement copy: %v", err)
		}
		if !claimed {
			break
		}
		if !nodes[item.NodeID] || copied[item.NodeID] || item.Target != target {
			t.Fatalf("copy claim = %+v, want one claim for each written node targeting %s", item, target)
		}
		copied[item.NodeID] = true
	}
	if len(copied) != len(nodes) {
		t.Fatalf("the queue contained copies of %d nodes, want %d", len(copied), len(nodes))
	}
}
