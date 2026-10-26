package integration_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// sequenceLoadTransactionTimeout is the production default of
	// FDB_TRANSACTION_TIMEOUT. It bounds all attempts of one counter write.
	sequenceLoadTransactionTimeout = 5 * time.Second
	// sequenceLoadDuration keeps the busy store writing the counter for longer
	// than one transaction timeout.
	sequenceLoadDuration = 8 * time.Second
	// sequenceLoadBusyWriters is the number of writers that keep the counter
	// lock of the busy store taken.
	sequenceLoadBusyWriters = 4
)

// sequenceLoadLog collects the counter values and the failures of every
// writer in the test.
type sequenceLoadLog struct {
	mutex    sync.Mutex
	values   []int64
	failures []string
}

func (log *sequenceLoadLog) record(writer string, value int64, err error) {
	log.mutex.Lock()
	defer log.mutex.Unlock()
	if err != nil {
		log.failures = append(log.failures, writer+": "+err.Error())
		return
	}
	log.values = append(log.values, value)
}

// TestSequenceWriterInSecondProcessFinishesWhileFirstProcessWritesTheCounter
// opens two stores on the same FoundationDB cluster, as two app processes do.
// Writers in the busy store increment one counter back to back for longer
// than the transaction timeout. A writer in the quiet store increments the
// same counter one call at a time in the same period. Every increment in
// both stores must succeed, and the stored values must be exactly 1 through N.
func TestSequenceWriterInSecondProcessFinishesWhileFirstProcessWritesTheCounter(t *testing.T) {
	ctx := integration.WithTestOperation(t, t.Context())
	clusterFile := testenv.FoundationDB(t)
	busy := openSequenceLoadStore(t, clusterFile)
	quiet := openSequenceLoadStore(t, clusterFile)
	orgID := uuid.Must(uuid.NewV7())
	counterKey := "two-process-load-" + uuid.NewString() + "-"
	log := &sequenceLoadLog{mutex: sync.Mutex{}, values: nil, failures: nil}
	stop := make(chan struct{})
	var busyWriters sync.WaitGroup
	for writer := range sequenceLoadBusyWriters {
		label := fmt.Sprintf("busy store writer %d", writer)
		busyWriters.Go(func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					log.record(label, 0, fmt.Errorf("panicked: %v", recovered))
				}
			}()
			for {
				select {
				case <-stop:
					return
				default:
				}
				value, err := busy.AllocateSequenceByKey(ctx, orgID, counterKey)
				log.record(label, value, err)
			}
		})
	}
	quietCalls, longestQuietCall := runQuietSequenceWriter(ctx, quiet, orgID, counterKey, log)
	close(stop)
	busyWriters.Wait()
	t.Logf("quiet store calls: %d; longest quiet store call: %s; counter values: %d",
		quietCalls, longestQuietCall, len(log.values))
	if len(log.failures) > 0 {
		t.Fatalf("every counter increment must succeed; %d failed:\n%s",
			len(log.failures), strings.Join(log.failures, "\n"))
	}
	requireOneThroughN(t, "counter values", log.values)
}

// runQuietSequenceWriter increments the counter through store one call at a
// time until sequenceLoadDuration ends. It returns the number of calls and
// the duration of the longest call.
func runQuietSequenceWriter(
	ctx context.Context, store *fdbadapter.NodeStore, orgID uuid.UUID, counterKey string, log *sequenceLoadLog,
) (int, time.Duration) {
	deadline := clock.Now().Add(sequenceLoadDuration)
	calls := 0
	longest := time.Duration(0)
	for clock.Now().Before(deadline) {
		started := clock.Now()
		value, err := store.AllocateSequenceByKey(ctx, orgID, counterKey)
		longest = max(longest, clock.Now().Sub(started))
		log.record("quiet store writer", value, err)
		calls++
	}
	return calls, longest
}

func openSequenceLoadStore(t *testing.T, clusterFile string) *fdbadapter.NodeStore {
	t.Helper()
	stores, err := fdbadapter.NewStores(clusterFile, sequenceLoadTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open the FoundationDB stores: %v", err)
	}
	return stores.Nodes
}
