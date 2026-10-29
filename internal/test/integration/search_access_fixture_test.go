package integration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
)

// shortLease is the lease of a claim that a test lets expire in real time.
const shortLease = 2 * time.Second

// classDrainSlices is the slice limit that existing callers pass to drainClass.
const classDrainSlices = 1000

// waitPastLeases waits in real time until every claim in works has expired.
func waitPastLeases(t *testing.T, works ...searchdomain.Work) {
	t.Helper()
	var latest time.Time
	for _, work := range works {
		if work.LeaseUntil.After(latest) {
			latest = work.LeaseUntil
		}
	}
	waitUntil(t, latest)
}

// newGrandchild stores a node of a new type that lists the moved child's
// type in CanLiveUnder, with one child_of edge to the moved child.
func newGrandchild(t *testing.T, stores *fdbadapter.Stores, moved movableChild, text string) searchFixture {
	t.Helper()
	grandchild := moved.Fixture
	grandchild.NodeID = uuid.Must(uuid.NewV7())
	grandchild.TypeKey = opaqueSearchKey("n")
	kind := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: grandchild.OrgID, Name: grandchild.TypeKey, TypeKey: grandchild.TypeKey,
		Slug: grandchild.TypeKey, PluralSlug: grandchild.TypeKey + "s", CanLiveUnder: []string{moved.Fixture.TypeKey},
	}
	if err := stores.NodeTypes.Set(WithTestOperation(t, t.Context()), kind); err != nil {
		t.Fatalf("store grandchild node type: %v", err)
	}
	props := map[string]json.RawMessage{grandchild.IncludedKey: mustJSON(text), grandchild.ExcludedKey: mustJSON(readerExcludedValue)}
	now := clock.Now().UTC()
	value := &node.Node{ID: grandchild.NodeID, OrgID: grandchild.OrgID, NodeType: grandchild.TypeKey, Name: searchFixtureName, Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: grandchild.NodeID, OrgID: grandchild.OrgID, NodeType: grandchild.TypeKey, Name: searchFixtureName, Props: props, CreatedAt: now, UpdatedAt: now}
	parent := &node.Relationship{OrgID: grandchild.OrgID, SourceID: grandchild.NodeID, RelationType: node.RelChildOf, TargetID: moved.Fixture.NodeID, CreatedBy: uuid.Nil, CreatedAt: now, Props: nil}
	if err := stores.Nodes.CreateAtomic(WithTestOperation(t, t.Context()), value, view, []*node.Relationship{parent}, nil, nil, nil); err != nil {
		t.Fatalf("create grandchild node: %v", err)
	}
	return grandchild
}

// claimAccessFor claims access work until it claims the work of nodeID. It
// yields every other claim before it returns.
func claimAccessFor(t *testing.T, store *fdbadapter.SearchWorkStore, nodeID uuid.UUID) searchdomain.Work {
	t.Helper()
	held := make([]searchdomain.Work, 0)
	defer func() {
		for _, work := range held {
			if err := store.Yield(WithTestOperation(t, t.Context()), work); err != nil {
				t.Errorf("yield inspected access work: %v", err)
			}
		}
	}()
	for range 100 {
		work, claimed, err := store.Claim(WithTestOperation(t, t.Context()), searchdomain.WorkClassAccess, "access-inspector", time.Minute)
		if err != nil || !claimed {
			t.Fatalf("claim access work of %s = claimed %t, err %v", nodeID, claimed, err)
		}
		if work.NodeID == nodeID {
			return work
		}
		held = append(held, work)
	}
	t.Fatalf("access work of %s was not claimable", nodeID)
	return searchdomain.Work{}
}

// drainClass claims and processes work of one class until it cannot claim more work.
// It returns the number of processed slices for each node.
// It fails the test if the class does not drain within maxSlices slices.
func drainClass(t *testing.T, store *fdbadapter.SearchWorkStore, worker *service.SearchWorker, class searchdomain.WorkClass, maxSlices int) map[uuid.UUID]int {
	t.Helper()
	sliceCounts := make(map[uuid.UUID]int)
	for range maxSlices {
		work, claimed, err := store.Claim(WithTestOperation(t, t.Context()), class, "class-driver", time.Minute)
		if err != nil {
			t.Fatalf("claim %s work: %v", class, err)
		}
		if !claimed {
			return sliceCounts
		}
		if err := worker.Process(WithTestOperation(t, t.Context()), work); err != nil {
			t.Fatalf("process %s work of %s: %v", class, work.NodeID, err)
		}
		sliceCounts[work.NodeID]++
	}
	t.Fatalf("%s work did not drain within %d slices", class, maxSlices)
	return sliceCounts
}

const (
	// engineRejectionText is the worker error text of a bulk item that
	// OpenSearch rejected with HTTP 429. The ML Commons memory circuit
	// breaker returns that status while the JVM heap is near its limit.
	engineRejectionText = "returned status 429 "
	// rejectedWorkWait is how long the loop keeps claiming after a rejection.
	// It includes the retry delay of released work, which equals the 10 s
	// operation timeout for an OpenSearch failure, and the time of one claim.
	rejectedWorkWait = 15 * time.Second
	// rejectionWindow bounds consecutive rejections without a successful slice.
	rejectionWindow = 3 * time.Minute
)

// runSearchWorkerWithin runs slices until no class has claimable work. limit
// bounds the slices that end without an error. A slice that OpenSearch
// rejects with HTTP 429 releases its work for the store's retry delay. The
// loop keeps claiming until that work is claimable again. The production loop
// retries the same way after its idle interval. Any other slice error fails
// the test. Rejections that continue past rejectionWindow also fail it.
func runSearchWorkerWithin(t *testing.T, worker *service.SearchWorker, limit int) {
	t.Helper()
	var rejectedSince, retryUntil time.Time
	for completed := 0; completed < limit; {
		claimed, err := worker.RunSlice(WithTestOperation(t, t.Context()))
		switch {
		case err != nil:
			rejectedSince = requireEngineRejection(t, err, rejectedSince)
			retryUntil = clock.Now().Add(rejectedWorkWait)
		case claimed:
			completed++
			rejectedSince = time.Time{}
		case clock.Now().Before(retryUntil):
			waitUntil(t, clock.Now().Add(waitForInterval))
		default:
			return
		}
	}
	t.Fatalf("search work did not converge within %d slices", limit)
}

// requireEngineRejection fails the test unless err is an OpenSearch HTTP 429
// rejection within rejectionWindow of rejectedSince. It returns the start of
// the current run of rejections.
func requireEngineRejection(t *testing.T, err error, rejectedSince time.Time) time.Time {
	t.Helper()
	if !strings.Contains(err.Error(), engineRejectionText) {
		t.Fatalf("run search worker slice: %v", err)
	}
	now := clock.Now()
	if rejectedSince.IsZero() {
		return now
	}
	if now.Sub(rejectedSince) > rejectionWindow {
		t.Fatalf("OpenSearch rejected search work for %s: %v", rejectionWindow, err)
	}
	return rejectedSince
}
