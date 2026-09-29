package integration

import (
	"strings"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/testenv"
)

// malformedNodeKeyText is the replacement scan failure for a primary node
// record key with three tuple values.
const malformedNodeKeyText = "node record key has 3 tuple values"

// TestSearchVerifyReportsStuckRebuildWork stores a primary node record key
// that the replacement scan cannot decode. Every rebuild slice then fails a
// counted FoundationDB read. After the attempt limit ops search verify must
// fail and list the rebuild item with its replacement ID, attempt count, and
// last error, and the rebuild item must keep retrying. The test then removes
// the malformed key. The replacement must finish, and verify must list no
// stuck work.
func TestSearchVerifyReportsStuckRebuildWork(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	drainSearchWork(t, fixture.Worker, 2000)
	malformed := writeMalformedNodeKey(t)

	request := searchdomain.BeginRebuild{Mode: searchdomain.ReplacementFull, PrimaryShards: 1, RoutingShards: 24, Replicas: 0, Restored: false, Reason: "test"}
	rebuild := beginRebuild(t, fixture, request)
	report, verifyErr := runUntilStuck(t, fixture, rebuild.ID.String())
	for _, stuck := range report.StuckWork {
		if stuck.ItemID != rebuild.ID.String() {
			continue
		}
		if stuck.Kind != string(searchdomain.WorkClassRebuild) || stuck.Attempts < 5 || !strings.Contains(stuck.LastError, malformedNodeKeyText) {
			t.Fatalf("stuck work = %+v, want rebuild work with at least 5 attempts and the scan failure", stuck)
		}
	}
	if verifyErr == nil || !strings.Contains(verifyErr.Error(), "rebuild work "+rebuild.ID.String()) || !strings.Contains(verifyErr.Error(), malformedNodeKeyText) {
		t.Fatalf("ops search verify error = %v, want the rebuild item %s and its last error", verifyErr, rebuild.ID)
	}
	current, found, err := fixture.Stores.SearchRebuilds(clock.Wall{}).CurrentRebuild(t.Context())
	if err != nil || !found || current.State != searchdomain.RebuildCopying {
		t.Fatalf("index replacement = %+v, found %t, error %v, want it still copying", current, found, err)
	}

	clearRawKey(t, malformed)
	runRebuildUntil(t, fixture, rebuildFinished)
	report, _ = runSearchVerifyCommand(t, fixture.Config)
	if report.StuckWorkItems != 0 {
		t.Fatalf("ops search verify lists %+v after the replacement finished", report.StuckWork)
	}
}

// writeMalformedNodeKey stores a primary node record key with three tuple
// values under an organization that no fixture uses. The replacement scan
// reads every primary node record key and fails to decode this one. It
// returns the key.
func writeMalformedNodeKey(t *testing.T) fdb.Key {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), testTransactionTimeout)
	if err != nil {
		t.Fatalf("open database to store a malformed node key: %v", err)
	}
	key := append(append([]byte{}, fdbadapter.TestPrefixRange()...), tuple.Tuple{"node_instance", uuid.Must(uuid.NewV7()).String(), "malformed"}.Pack()...)
	if _, err := database.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(fdb.Key(key), []byte("{}"))
		return nil, nil
	}); err != nil {
		t.Fatalf("store malformed node key: %v", err)
	}
	return fdb.Key(key)
}

// clearRawKey clears one key.
func clearRawKey(t *testing.T, key fdb.Key) {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), testTransactionTimeout)
	if err != nil {
		t.Fatalf("open database to clear a key: %v", err)
	}
	if _, err := database.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Clear(key)
		return nil, nil
	}); err != nil {
		t.Fatalf("clear key: %v", err)
	}
}

// runUntilStuck runs worker slices until ops search verify lists the work
// item itemID as stuck, and ignores slice failures. It fails the test after
// exclusionDeadline.
func runUntilStuck(t *testing.T, fixture queryFixture, itemID string) (searchVerifyReport, error) {
	t.Helper()
	deadline := clock.Now().Add(exclusionDeadline)
	for clock.Now().Before(deadline) {
		claimed, _ := fixture.Worker.RunSlice(t.Context())
		if claimed {
			continue
		}
		report, verifyErr := runSearchVerifyCommand(t, fixture.Config)
		for _, stuck := range report.StuckWork {
			if stuck.ItemID == itemID {
				return report, verifyErr
			}
		}
		waitUntil(t, clock.Now().Add(exclusionPollInterval))
	}
	t.Fatalf("ops search verify did not list work item %s within %s", itemID, exclusionDeadline)
	return searchVerifyReport{}, nil
}
