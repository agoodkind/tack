package integration_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	// overflowGrantNodes is the number of nodes the overflow test moves
	// during the affected pass. This count exceeds affectedSetBound.
	overflowGrantNodes = 260
	// affectedSetBound is the production bound on the affected
	// set. The overflow test exceeds this bound.
	affectedSetBound = 256
	// heldAccessLease is the lease duration for an access work claim that a test
	// keeps.
	heldAccessLease = 10 * time.Minute
	// holdClaimAttempts limits the claims a test makes while looking for one
	// access work item.
	holdClaimAttempts = 100
)

// holdAccessWork claims access work until it claims the work of nodeID with
// heldAccessLease. It yields every other claim and returns the claimed work.
func holdAccessWork(t *testing.T, fixture rolloutFixture, nodeID uuid.UUID) searchdomain.Work {
	t.Helper()
	store := fixture.stores.SearchWork(clock.Wall{})
	others := make([]searchdomain.Work, 0)
	defer func() {
		for _, work := range others {
			if err := store.Yield(rolloutContext(t), work); err != nil {
				t.Errorf("Yielding access work for node %s failed: %v", work.NodeID, err)
			}
		}
	}()
	for range holdClaimAttempts {
		work, err := store.Claim(rolloutContext(t), searchdomain.WorkClassAccess, "rollout-access-holder", heldAccessLease)
		if err != nil {
			t.Fatalf("Claiming access work while looking for node %s failed: %v", nodeID, err)
		}
		if work.NodeID == nodeID {
			return work
		}
		others = append(others, work)
	}
	t.Fatalf("Access work for node %s did not become claimable within %d claims", nodeID, holdClaimAttempts)
	return searchdomain.Work{}
}

func releaseAccessWork(t *testing.T, fixture rolloutFixture, work searchdomain.Work) {
	t.Helper()
	if err := fixture.stores.SearchWork(clock.Wall{}).Yield(rolloutContext(t), work); err != nil {
		t.Fatalf("Yielding access work for node %s failed: %v", work.NodeID, err)
	}
}
