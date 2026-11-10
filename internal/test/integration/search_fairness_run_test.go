package integration_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// fairnessShortText is the text the backlog indexes before linking or
	// deleting each node. It fits on one page.
	fairnessShortText = "fairness short text"
	// sliceCompletedRecord is the Debug message the worker logs after
	// each slice.
	sliceCompletedRecord = "search.worker.slice_completed"
	// ageEnqueuedPosition is the enqueue time position in a search_age key.
	ageEnqueuedPosition = 3
)

// fairnessSlice records a claimed slice's class and duration from the
// worker log and the pending state the test reads after that slice.
type fairnessSlice struct {
	class    string
	duration time.Duration
	// accessPending, cleanupPending, and livePending report whether each
	// class still has age keys after the slice. accessAge is the oldest
	// pending access age, or zero when access does not have age keys.
	accessPending, cleanupPending, livePending bool
	accessAge                                  time.Duration
}

// sliceLogRecord contains the worker log fields the test reads. The JSON
// handler writes the slice duration in nanoseconds.
type sliceLogRecord struct {
	Message  string `json:"msg"`
	Class    string `json:"class"`
	Duration int64  `json:"duration"`
}

// buildFairnessBacklog stores and indexes accessNodes and deletedNodes
// nodes with fairnessShortText. It links the first access node to each
// other access node in a separate transaction, scheduling access work for
// both endpoints. It deletes the remaining indexed nodes, scheduling
// cleanup to retire their pages. It then stores liveNodes nodes with
// liveText, scheduling live work.
func buildFairnessBacklog(t *testing.T, env *migrationEnv, accessNodes, deletedNodes, liveNodes int, liveText string) {
	t.Helper()
	indexed := env.putNodes(t, accessNodes+deletedNodes, fairnessShortText)
	env.runIdle(t)
	hub := indexed[0]
	for _, spoke := range indexed[1:accessNodes] {
		relationship := &node.Relationship{
			OrgID: env.OrgID, SourceID: hub, RelationType: uniqueKey("r"), TargetID: spoke,
			CreatedBy: uuid.Nil, CreatedAt: clock.Now().UTC(), Props: nil,
		}
		if err := env.Stores.Relationships.Add(integration.WithTestOperation(t, t.Context()), relationship); err != nil {
			t.Fatalf("link hub %s to node %s: %v", hub, spoke, err)
		}
	}
	for _, nodeID := range indexed[accessNodes:] {
		if err := env.Stores.Nodes.Delete(integration.WithTestOperation(t, t.Context()), env.OrgID, nodeID); err != nil {
			t.Fatalf("delete node %s: %v", nodeID, err)
		}
	}
	env.putNodes(t, liveNodes, liveText)
}

// runFairnessSlices runs worker slices with a Debug logger until access
// and cleanup lack age keys, the run claims limit slices, or the elapsed
// time exceeds deadline. After each claimed slice, it records the logged
// class and duration and reads access, cleanup, and live age keys. It logs
// slice errors and continues. It waits idleClaimPause when a slice does
// not claim work.
func runFairnessSlices(t *testing.T, worker *service.SearchWorker, limit int, deadline time.Duration) []fairnessSlice {
	t.Helper()
	database := openPrefixedDatabase(t)
	started := clock.Now()
	slices := make([]fairnessSlice, 0, limit)
	for len(slices) < limit && clock.Since(started) < deadline {
		var logged bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
		claimed, err := worker.RunSlice(telemetry.WithLogger(integration.WithTestOperation(t, t.Context()), logger))
		if err != nil {
			t.Logf("run a fairness slice: %v", err)
		}
		if !claimed {
			waitUntilTime(t, clock.Now().Add(idleClaimPause))
			continue
		}
		slice := completedSlice(t, &logged)
		slice.accessAge, slice.accessPending = oldestPendingAge(t, database, searchdomain.WorkClassAccess)
		_, slice.cleanupPending = oldestPendingAge(t, database, searchdomain.WorkClassCleanup)
		_, slice.livePending = oldestPendingAge(t, database, searchdomain.WorkClassLive)
		slices = append(slices, slice)
		if !slice.accessPending && !slice.cleanupPending {
			return slices
		}
	}
	return slices
}

// completedSlice returns the class and duration from the completed slice
// record in logged. It fails the test if logged omits that record.
func completedSlice(t *testing.T, logged *bytes.Buffer) fairnessSlice {
	t.Helper()
	decoder := json.NewDecoder(logged)
	for decoder.More() {
		var record sliceLogRecord
		if err := decoder.Decode(&record); err != nil {
			t.Fatalf("decode a worker log record: %v", err)
		}
		if record.Message == sliceCompletedRecord {
			return fairnessSlice{
				class: record.Class, duration: time.Duration(record.Duration),
				accessPending: false, cleanupPending: false, livePending: false, accessAge: 0,
			}
		}
	}
	t.Fatalf("the claimed slice did not log %s", sliceCompletedRecord)
	return fairnessSlice{}
}

// oldestPendingAge reads every age key of class under the test prefix and
// returns the oldest pending age and whether any age key exists. It returns
// zero and false when the class does not have age keys.
func oldestPendingAge(t *testing.T, database fdb.Database, class searchdomain.WorkClass) (time.Duration, bool) {
	t.Helper()
	prefix := fdbadapter.TestPrefixRange()
	keyRange, err := fdb.PrefixRange(prefixedKey(prefix, tuple.Tuple{"search_age", string(class)}))
	if err != nil {
		t.Fatalf("create the age key range of class %s: %v", class, err)
	}
	read, err := database.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		t.Fatalf("read the age keys of class %s: %v", class, err)
	}
	entries, _ := read.([]fdb.KeyValue)
	if len(entries) == 0 {
		return 0, false
	}
	oldest := int64(0)
	for position, entry := range entries {
		elements, err := tuple.Unpack(entry.Key[len(prefix):])
		if err != nil || len(elements) <= ageEnqueuedPosition {
			t.Fatalf("decode age key %x of class %s: %v", entry.Key, class, err)
		}
		enqueued, isInteger := elements[ageEnqueuedPosition].(int64)
		if !isInteger {
			t.Fatalf("age key %v of class %s has enqueue time %T, want an integer", elements, class, elements[ageEnqueuedPosition])
		}
		if position == 0 || enqueued < oldest {
			oldest = enqueued
		}
	}
	return clock.Since(time.Unix(0, oldest)), true
}

// requireAccessCaughtUp requires the last sample to show access and cleanup
// without age keys while live still has age keys. Its failure message
// reports the largest oldest-access age the run observed.
func requireAccessCaughtUp(t *testing.T, slices []fairnessSlice) {
	t.Helper()
	if len(slices) == 0 {
		t.Fatal("the fairness worker did not claim any work")
	}
	largestAge := time.Duration(0)
	for _, slice := range slices {
		largestAge = max(largestAge, slice.accessAge)
	}
	last := slices[len(slices)-1]
	if last.accessPending || last.cleanupPending || !last.livePending {
		t.Fatalf("after %d slices access pending %t, cleanup pending %t, live pending %t, and the oldest access age rose to %s; want access and cleanup finished while live work remains",
			len(slices), last.accessPending, last.cleanupPending, last.livePending, largestAge)
	}
}
