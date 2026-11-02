package integration_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	scanFailureNodeFamily = "node_instance"
	scanFailureOrg        = "~"
	scanFailureOperation  = "schedule replacement copies"
	scanFailureRetryLimit = 30 * time.Second
	scanFailureRetryPause = 200 * time.Millisecond
)

// TestSearchRebuildScanResumesAfterFailedRead verifies that a malformed node key
// makes a scan step fail through SearchWorker.Process while the rebuild remains
// copying at its saved cursor. After the test clears the key and the retry delay
// ends, the next step completes the scan and the copy queue returns one claim
// for each written node.
func TestSearchRebuildScanResumesAfterFailedRead(t *testing.T) {
	stores := newIdleClaimStores(t)
	newIdleClaimWorkStore(t, stores)
	nodes := writeScanRestartNodes(t, stores)
	stoppedWork, _ := stopRebuildAfterFirstScanStep(t, stores)
	waitUntilTime(t, stoppedWork.LeaseUntil)
	malformed := writeMalformedNodeKey(t)
	worker := newScanRestartWorker(t, stores)
	ctx := integration.WithTestOperation(t, t.Context())
	if err := worker.Process(ctx, claimScanWork(t, stores)); err != nil {
		t.Fatalf("run the scan step before the malformed key: %v", err)
	}
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	before := currentIdleClaimRebuild(t, rebuilds)
	if err := worker.Process(ctx, claimScanWork(t, stores)); err == nil || !strings.Contains(err.Error(), scanFailureOperation) {
		t.Fatalf("scan step over the malformed key returned %v, want an error from %q", err, scanFailureOperation)
	}
	after := currentIdleClaimRebuild(t, rebuilds)
	if after.State != searchdomain.RebuildCopying || after.ScanComplete || after.ScanCursor != before.ScanCursor {
		t.Fatalf("rebuild after the failed scan step = %+v, want copying at cursor %q", after, before.ScanCursor)
	}
	clearMalformedNodeKey(t, malformed)
	runScanStepAfterRetryDelay(t, stores, worker)
	finished := currentIdleClaimRebuild(t, rebuilds)
	if !finished.ScanComplete {
		t.Fatalf("rebuild after the retried scan step = %+v, want a complete scan", finished)
	}
	requireEveryNodeCopiedOnce(t, stores, finished.TargetIndex, nodes)
}

func writeMalformedNodeKey(t *testing.T) fdb.Key {
	t.Helper()
	database := openScanFailureDatabase(t)
	key := fdb.Key(append(bytes.Clone(activeTestPrefix(t)), tuple.Tuple{scanFailureNodeFamily, scanFailureOrg, "malformed"}.Pack()...))
	if _, err := database.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(key, []byte{})
		return nil, nil
	}); err != nil {
		t.Fatalf("write the malformed node key: %v", err)
	}
	return key
}

func clearMalformedNodeKey(t *testing.T, key fdb.Key) {
	t.Helper()
	database := openScanFailureDatabase(t)
	if _, err := database.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Clear(key)
		return nil, nil
	}); err != nil {
		t.Fatalf("clear the malformed node key: %v", err)
	}
}

func openScanFailureDatabase(t *testing.T) fdb.Database {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), idleClaimTransactionTimeout)
	if err != nil {
		t.Fatalf("open FoundationDB: %v", err)
	}
	return database
}

func claimScanWork(t *testing.T, stores *fdbadapter.Stores) searchdomain.Work {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	work, claimed, err := stores.SearchWork(clock.Wall{}).Claim(ctx, searchdomain.WorkClassRebuild, "scan-failure-owner", scanRestartLease)
	if err != nil || !claimed {
		t.Fatalf("claim replacement work: claimed %t, error %v", claimed, err)
	}
	return work
}

func runScanStepAfterRetryDelay(t *testing.T, stores *fdbadapter.Stores, worker *service.SearchWorker) {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	deadline := time.Now().Add(scanFailureRetryLimit)
	for time.Now().Before(deadline) {
		work, claimed, err := stores.SearchWork(clock.Wall{}).Claim(ctx, searchdomain.WorkClassRebuild, "scan-failure-owner", scanRestartLease)
		if err != nil {
			t.Fatalf("claim replacement work after the failure: %v", err)
		}
		if claimed {
			if err := worker.Process(ctx, work); err != nil {
				t.Fatalf("retry the scan step: %v", err)
			}
			return
		}
		time.Sleep(scanFailureRetryPause)
	}
	t.Fatalf("replacement work stayed unclaimable for %s after the failure", scanFailureRetryLimit)
}
