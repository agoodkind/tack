package integration_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// maintenanceDeadline bounds each wait for repeated failures and for the
	// recovery. Five failures take at least four retry delays of five seconds.
	maintenanceDeadline = 3 * time.Minute
	// maintenancePoll spaces the verify runs while released work waits.
	maintenancePoll = time.Second
)

// maintenanceStuckWork is one work item that ops search verify lists at or
// past the attempt limit.
type maintenanceStuckWork struct {
	Kind      string `json:"kind"`
	ItemID    string `json:"item_id"`
	Attempts  int64  `json:"attempts"`
	LastError string `json:"last_error"`
}

// maintenanceReport is the stuck work listing that ops search verify writes.
type maintenanceReport struct {
	StuckWorkItems int                    `json:"stuck_work_items"`
	StuckWork      []maintenanceStuckWork `json:"stuck_work"`
}

// stuck returns the listing of item when it has at least
// maintenancePastLimit attempts.
func (report maintenanceReport) stuck(item maintenanceItem) (maintenanceStuckWork, bool) {
	for _, stuck := range report.StuckWork {
		if stuck.Kind == string(item.Class) && stuck.ItemID == item.itemID() && stuck.Attempts >= maintenancePastLimit {
			return stuck, true
		}
	}
	return maintenanceStuckWork{}, false
}

// attemptLimitRecord is one decoded worker log record.
type attemptLimitRecord struct {
	Message  string `json:"msg"`
	Class    string `json:"class"`
	ItemID   string `json:"item_id"`
	Attempts int64  `json:"attempts"`
	Err      string `json:"err"`
}

// runMaintenanceVerify runs the audited ops search verify command and
// decodes its stuck work listing. It returns the command error separately.
func runMaintenanceVerify(t *testing.T, fixture integration.QueryFixture) (maintenanceReport, error) {
	t.Helper()
	output, runErr := runAuditedCommand(t, fixture.Config, "ops", "search", "verify")
	var envelope struct {
		Result maintenanceReport `json:"result"`
	}
	if err := json.NewDecoder(bytes.NewReader(output)).Decode(&envelope); err != nil {
		t.Fatalf("decode the ops search verify listing (command error %v): %v", runErr, err)
	}
	return envelope.Result, runErr
}

// runUntilMaintenanceClear runs worker slices until no item has an attempt
// count and the access rollout of the items is stable. It fails the test
// when a slice fails or after maintenanceDeadline.
func runUntilMaintenanceClear(t *testing.T, fixture integration.QueryFixture, items []maintenanceItem) {
	t.Helper()
	rollouts := fixture.Stores.SearchRollouts(clock.Wall{}, fixture.Stores.SearchPolicySet())
	deadline := clock.Now().Add(maintenanceDeadline)
	for {
		remaining := make([]maintenanceItem, 0, len(items))
		for _, item := range items {
			if len(readPrefixedValue(t, tuple.Tuple{"search_attempt", string(item.Class), item.OrgID.String(), item.NodeID.String()})) > 0 {
				remaining = append(remaining, item)
			}
		}
		rollout, err := rollouts.Current(integration.WithTestOperation(t, t.Context()), items[0].OrgID)
		if err != nil {
			t.Fatalf("read the access rollout of %s: %v", items[0].OrgID, err)
		}
		if len(remaining) == 0 && rollout.Phase == searchdomain.AccessStable {
			return
		}
		if clock.Now().After(deadline) {
			t.Fatalf("work items %+v kept an attempt count and the rollout stayed in phase %s for %s after the reads recovered", remaining, rollout.Phase, maintenanceDeadline)
		}
		claimed, err := fixture.Worker.RunSlice(integration.WithTestOperation(t, t.Context()))
		if err != nil {
			t.Fatalf("run search worker slice after the reads recovered: %v", err)
		}
		if !claimed {
			waitUntilTime(t, clock.Now().Add(maintenancePoll))
		}
	}
}

// openPrefixedDatabase opens the test FoundationDB cluster.
func openPrefixedDatabase(t *testing.T) fdb.Database {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), recoveryStepTimeout)
	if err != nil {
		t.Fatalf("open FoundationDB: %v", err)
	}
	return database
}

// setPrefixedValue stores value under the prefixed key of values.
func setPrefixedValue(t *testing.T, values tuple.Tuple, value []byte) {
	t.Helper()
	key := prefixedKey(fdbadapter.TestPrefixRange(), values)
	if _, err := openPrefixedDatabase(t).Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(key, value)
		return nil, nil
	}); err != nil {
		t.Fatalf("store key %v: %v", values, err)
	}
}

// clearPrefixedKey clears the prefixed key of values.
func clearPrefixedKey(t *testing.T, values tuple.Tuple) {
	t.Helper()
	key := prefixedKey(fdbadapter.TestPrefixRange(), values)
	if _, err := openPrefixedDatabase(t).Transact(func(tr fdb.Transaction) (any, error) {
		tr.Clear(key)
		return nil, nil
	}); err != nil {
		t.Fatalf("clear key %v: %v", values, err)
	}
}

// readPrefixedValue returns the value stored under the prefixed key of
// values.
func readPrefixedValue(t *testing.T, values tuple.Tuple) []byte {
	t.Helper()
	key := prefixedKey(fdbadapter.TestPrefixRange(), values)
	value, err := openPrefixedDatabase(t).ReadTransact(func(tr fdb.ReadTransaction) (any, error) { return tr.Get(key).Get() })
	if err != nil {
		t.Fatalf("read key %v: %v", values, err)
	}
	stored, _ := value.([]byte)
	return stored
}
