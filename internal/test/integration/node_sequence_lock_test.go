package integration_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// sequenceLockTransactionTimeout limits each FoundationDB transaction of
	// the store in the lock test.
	sequenceLockTransactionTimeout = 5 * time.Second
	// queuedSequenceWriters is the number of allocations that wait for one
	// counter lock before the canceled allocation starts.
	queuedSequenceWriters = 32
	// queuedWritersHeadStart is the time the queued allocations get to start
	// before the canceled allocation starts.
	queuedWritersHeadStart = 20 * time.Millisecond
	// canceledWaitTimeout ends the context of the allocation that waits
	// behind the queued allocations.
	canceledWaitTimeout = 5 * time.Millisecond
)

// TestNodeStoreSequenceLockWaitEndsWithContext starts allocations on one
// counter, then starts one more allocation with a context that ends after a
// few milliseconds. The last allocation must return the context error before
// every queued allocation finishes, and the queued allocations must store
// exactly 1 through N.
func TestNodeStoreSequenceLockWaitEndsWithContext(t *testing.T) {
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), sequenceLockTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	ctx := integration.WithTestOperation(t, t.Context())
	orgID := uuid.New()
	counterKey := "LOCK-"
	var completed atomic.Int64
	values := make([]int64, queuedSequenceWriters)
	failures := make([]error, queuedSequenceWriters)
	var group sync.WaitGroup
	for index := range queuedSequenceWriters {
		group.Go(func() {
			defer completed.Add(1)
			defer func() {
				if recovered := recover(); recovered != nil {
					failures[index] = fmt.Errorf("allocation %d panicked: %v", index, recovered)
				}
			}()
			values[index], failures[index] = stores.Nodes.AllocateSequenceByKey(ctx, orgID, counterKey)
		})
	}
	time.Sleep(queuedWritersHeadStart)
	waitContext, cancel := context.WithTimeout(ctx, canceledWaitTimeout)
	defer cancel()
	_, waitErr := stores.Nodes.AllocateSequenceByKey(waitContext, orgID, counterKey)
	completedAtReturn := completed.Load()
	group.Wait()
	if !errors.Is(waitErr, context.DeadlineExceeded) {
		t.Fatalf("allocation with an ended context = %v; want an error that wraps %v", waitErr, context.DeadlineExceeded)
	}
	if completedAtReturn >= queuedSequenceWriters {
		t.Fatalf("allocation with an ended context returned after all %d queued allocations finished", queuedSequenceWriters)
	}
	if err := errors.Join(failures...); err != nil {
		t.Fatalf("every queued allocation must succeed: %v", err)
	}
	requireOneThroughN(t, "queued allocation values", values)
}
