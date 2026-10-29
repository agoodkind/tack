package integration_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// oldestBucketChoices sets the concurrent claim count and the claim
	// limit for finding the oldest item. It matches claimBucketChoices.
	oldestBucketChoices = 4
	// oldestBucketNodes sets the live node count, enough to populate
	// well over oldestBucketMinimum buckets.
	oldestBucketNodes = 160
	// oldestBucketMinimum sets the fewest buckets the live work must use.
	oldestBucketMinimum = 64
	// oldestClaimLease sets the lease duration for each test claim.
	oldestClaimLease = time.Minute
)

// TestSearchClaimPrefersOldestBuckets writes nodes one at a time with
// distinct live enqueue times across at least oldestBucketMinimum buckets.
// It requires oldestBucketChoices concurrent claims to lease items from
// more than one bucket, then yields them. A new store must lease the oldest
// live item within oldestBucketChoices claims. A clock-chosen bucket fails
// this test because it usually starts among newer items in the many
// pending buckets.
func TestSearchClaimPrefersOldestBuckets(t *testing.T) {
	stores := integration.SetupTestEnv(t).Stores
	stores.EnableSearchWork()
	if err := stores.InitializeSearchIndex(integration.WithTestOperation(t, t.Context()), uniqueKey("claim-oldest-")); err != nil {
		t.Fatalf("record serving index: %v", err)
	}
	orgID := uuid.Must(uuid.NewV7())
	typeKey := uniqueKey("n")
	kind := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: typeKey, TypeKey: typeKey, Slug: typeKey,
		PluralSlug: typeKey + "s", Features: node.Features{node.FeatureIsEntryPoint},
	}
	if err := stores.NodeTypes.Set(integration.WithTestOperation(t, t.Context()), kind); err != nil {
		t.Fatalf("store node type: %v", err)
	}
	buckets := map[int64]struct{}{}
	for range oldestBucketNodes {
		nodeID := uuid.Must(uuid.NewV7())
		now := clock.Now().UTC()
		value := &node.Node{ID: nodeID, OrgID: orgID, NodeType: typeKey, Name: "Oldest bucket node", CreatedAt: now, UpdatedAt: now}
		view := &node.NodeView{ID: nodeID, OrgID: orgID, NodeType: typeKey, Name: "Oldest bucket node", CreatedAt: now, UpdatedAt: now}
		if err := stores.Nodes.Set(integration.WithTestOperation(t, t.Context()), value, view); err != nil {
			t.Fatalf("store node %s: %v", nodeID, err)
		}
		buckets[workBucket(orgID, nodeID)] = struct{}{}
	}
	if len(buckets) < oldestBucketMinimum {
		t.Fatalf("%d nodes use %d buckets, want at least %d", oldestBucketNodes, len(buckets), oldestBucketMinimum)
	}
	requireConcurrentClaimSpread(t, stores.SearchWork(clock.Wall{}))

	store := stores.SearchWork(clock.Wall{})
	first := make([]searchdomain.Work, 0, oldestBucketChoices)
	claimed := make([]searchdomain.Work, 0, oldestBucketNodes)
	for {
		work, found, err := store.Claim(integration.WithTestOperation(t, t.Context()), searchdomain.WorkClassLive, "oldest-claimer", oldestClaimLease)
		if err != nil {
			t.Fatalf("claim live work: %v", err)
		}
		if !found {
			break
		}
		if len(first) < oldestBucketChoices {
			first = append(first, work)
		}
		claimed = append(claimed, work)
	}
	if len(claimed) != oldestBucketNodes {
		t.Fatalf("claimed %d live items, want %d", len(claimed), oldestBucketNodes)
	}
	oldest := claimed[0]
	for _, work := range claimed {
		if work.EnqueuedAt.Before(oldest.EnqueuedAt) {
			oldest = work
		}
	}
	for _, work := range first {
		if work.NodeID == oldest.NodeID {
			return
		}
	}
	t.Fatalf("the first %d claims took nodes %v, want the oldest live item %s enqueued at %s",
		oldestBucketChoices, workNodes(first), oldest.NodeID, oldest.EnqueuedAt)
}

// requireConcurrentClaimSpread starts oldestBucketChoices claims together
// on one store and requires each to lease an item. It requires the items
// to use more than one bucket and yields every claim.
func requireConcurrentClaimSpread(t *testing.T, store *fdbadapter.SearchWorkStore) {
	t.Helper()
	claims := make([]searchdomain.Work, oldestBucketChoices)
	failures := make([]error, oldestBucketChoices)
	start := make(chan struct{})
	var group sync.WaitGroup
	for position := range oldestBucketChoices {
		group.Go(func() {
			<-start
			work, claimed, err := store.Claim(integration.WithTestOperation(t, t.Context()), searchdomain.WorkClassLive, uuid.NewString(), oldestClaimLease)
			if err == nil && !claimed {
				err = errors.New("the claim did not lease an item")
			}
			claims[position], failures[position] = work, err
		})
	}
	close(start)
	group.Wait()
	spread := map[int64]struct{}{}
	for position, work := range claims {
		if failures[position] != nil {
			t.Fatalf("concurrent claim %d: %v", position, failures[position])
		}
		spread[workBucket(work.OrgID, work.NodeID)] = struct{}{}
		if err := store.Yield(integration.WithTestOperation(t, t.Context()), work); err != nil {
			t.Fatalf("yield concurrent claim of %s: %v", work.NodeID, err)
		}
	}
	if len(spread) < 2 {
		t.Fatalf("%d concurrent claims took nodes %v from one bucket, want more than one bucket", oldestBucketChoices, workNodes(claims))
	}
}

// workNodes returns the node ID of each work item in order.
func workNodes(claims []searchdomain.Work) []uuid.UUID {
	nodes := make([]uuid.UUID, 0, len(claims))
	for _, work := range claims {
		nodes = append(nodes, work.NodeID)
	}
	return nodes
}
