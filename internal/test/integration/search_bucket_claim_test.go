package integration_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	// claimScanLimit matches the production limit on age keys per bucket and
	// sets the expected number of claims while all leases remain active.
	claimScanLimit  = 32
	crowdedOverflow = 8
	crowdedText     = "crowded bucket text"
	stalledOwner    = "stalled-search-worker"
	stalledLease    = 20 * time.Second
	stalledSlices   = 3
)

// requireBoundedClaimScan requires claims and worker slices to lease only the
// oldest claimScanLimit items in a crowded bucket while those leases remain
// active. It requires newer items to remain pending until the leases expire,
// then requires the worker to finish all live work in the bucket.
func requireBoundedClaimScan(t *testing.T, fixture parentEdgeFixture) {
	t.Helper()
	crowded := writeCrowdedBucket(t, fixture)
	bucket := workBucket(fixture.kind.orgID, crowded[0])
	ordered := liveNodesInBucket(t, fixture, ageFamily, bucket)
	if !sameNodeSet(ordered, crowded) {
		t.Fatalf("bucket %d lists live work for nodes %v, want %v", bucket, ordered, crowded)
	}
	claimed, leaseUntil := claimUntilNoWork(t, fixture)
	window, beyond := ordered[:claimScanLimit], ordered[claimScanLimit:]
	if !sameNodeSet(claimed, window) {
		t.Fatalf("an owner that keeps every lease claimed %v, want the %d oldest items %v", claimed, claimScanLimit, window)
	}
	requireLeasesOnly(t, fixture, bucket, window)
	for range stalledSlices {
		runSliceOrWait(t, fixture)
	}
	requireLeasesOnly(t, fixture, bucket, window)
	pending := liveNodesInBucket(t, fixture, ageFamily, bucket)
	for _, nodeID := range beyond {
		if !slices.Contains(pending, nodeID) {
			t.Fatalf("the search worker finished item %s beyond the first %d items of bucket %d", nodeID, claimScanLimit, bucket)
		}
	}
	waitUntilTime(t, leaseUntil)
	fixture.drain(t)
	if remaining := liveNodesInBucket(t, fixture, ageFamily, bucket); len(remaining) != 0 {
		t.Fatalf("live work for nodes %v remains after the stalled leases ended", remaining)
	}
}

// writeCrowdedBucket creates more leaf nodes than claimScanLimit in one work
// bucket, accepting only node IDs that hash to that bucket.
func writeCrowdedBucket(t *testing.T, fixture parentEdgeFixture) []uuid.UUID {
	t.Helper()
	orgID := fixture.kind.orgID
	bucket := workBucket(orgID, uuid.Must(uuid.NewV7()))
	crowded := make([]uuid.UUID, 0, claimScanLimit+crowdedOverflow)
	for len(crowded) < claimScanLimit+crowdedOverflow {
		candidate := uuid.Must(uuid.NewV7())
		if workBucket(orgID, candidate) != bucket {
			continue
		}
		storeLeafWithID(t, fixture, candidate)
		crowded = append(crowded, candidate)
	}
	return crowded
}

// storeLeafWithID uses NodeRepository.CreateAtomic to create a leaf with
// nodeID, its view, and its child relationship to fixture.firstEntry.
func storeLeafWithID(t *testing.T, fixture parentEdgeFixture, nodeID uuid.UUID) {
	t.Helper()
	kind := fixture.kind
	props := map[string]json.RawMessage{kind.included: parentEdgeJSON(t, crowdedText), kind.excluded: parentEdgeJSON(t, "excluded")}
	now := clock.Now().UTC()
	created := &node.Node{ID: nodeID, OrgID: kind.orgID, NodeType: kind.typeKey, Name: crowdedText, Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: nodeID, OrgID: kind.orgID, NodeType: kind.typeKey, Name: crowdedText, Props: props, CreatedAt: now, UpdatedAt: now}
	parent := &node.Relationship{OrgID: kind.orgID, SourceID: nodeID, RelationType: node.RelChildOf, TargetID: fixture.firstEntry, CreatedAt: now}
	if err := fixture.stores.Nodes.CreateAtomic(t.Context(), created, view, []*node.Relationship{parent}, nil, nil, nil); err != nil {
		t.Fatalf("store node %s: %v", nodeID, err)
	}
}

func liveNodesInBucket(t *testing.T, fixture parentEdgeFixture, family string, bucket int64) []uuid.UUID {
	t.Helper()
	layout := workKeyLayouts[family]
	nodes := make([]uuid.UUID, 0, claimScanLimit+crowdedOverflow)
	for _, key := range stateKeysOf(readStateKeys(t, fixture), family) {
		if key.text(t, layout.class) == liveClass && key.integer(t, layout.bucket) == bucket {
			nodes = append(nodes, uuid.MustParse(key.text(t, layout.node)))
		}
	}
	return nodes
}

func requireLeasesOnly(t *testing.T, fixture parentEdgeFixture, bucket int64, window []uuid.UUID) {
	t.Helper()
	if leases := liveNodesInBucket(t, fixture, claimFamily, bucket); !sameNodeSet(leases, window) {
		t.Fatalf("bucket %d stores leases for nodes %v, want one lease for each claimed item %v", bucket, leases, window)
	}
}

// claimUntilNoWork claims live work as stalledOwner without completing it
// until Claim returns ErrNoWork. It returns the claimed node IDs and the last
// claim's lease deadline, or fails if every allowed attempt returns work.
func claimUntilNoWork(t *testing.T, fixture parentEdgeFixture) ([]uuid.UUID, time.Time) {
	t.Helper()
	store := fixture.stores.SearchWork(clock.Wall{})
	claimed := make([]uuid.UUID, 0, claimScanLimit)
	var leaseUntil time.Time
	for range claimScanLimit + crowdedOverflow + 1 {
		work, err := store.Claim(t.Context(), searchdomain.WorkClassLive, stalledOwner, stalledLease)
		if errors.Is(err, searchdomain.ErrNoWork) {
			return claimed, leaseUntil
		}
		if err != nil {
			t.Fatalf("claim live work as %s: %v", stalledOwner, err)
		}
		claimed, leaseUntil = append(claimed, work.NodeID), work.LeaseUntil
	}
	t.Fatalf("owner %s claimed %d items, and the next claim still returned work", stalledOwner, len(claimed))
	return nil, leaseUntil
}

func sameNodeSet(left, right []uuid.UUID) bool {
	if len(left) != len(right) {
		return false
	}
	for _, nodeID := range left {
		if !slices.Contains(right, nodeID) {
			return false
		}
	}
	return true
}
