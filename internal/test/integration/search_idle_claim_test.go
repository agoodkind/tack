package integration_test

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/test/integration"
)

const (
	idleClaimOwner = "idle-claim-inspector"
	idleClaimLease = time.Minute
)

var idleClaimClasses = []searchdomain.WorkClass{
	searchdomain.WorkClassLive, searchdomain.WorkClassAccess, searchdomain.WorkClassCleanup,
	searchdomain.WorkClassRescan, searchdomain.WorkClassRollout, searchdomain.WorkClassRebuild,
	searchdomain.WorkClassCopy,
}

// TestSearchWorkEmptyClassesThenEveryClassClaims calls Claim once for each
// of the seven work classes in [idleClaimClasses] against an empty store.
// It requires each call to return without an item or an error. It then
// schedules pending work in every class through [scheduleEveryClass].
// The next claim of each class must lease an item of that class with a node
// ID that the helper returned. The test uses real FoundationDB under a
// per-test key prefix.
func TestSearchWorkEmptyClassesThenEveryClassClaims(t *testing.T) {
	stores := newIdleClaimStores(t)
	store := newIdleClaimWorkStore(t, stores)
	for _, class := range idleClaimClasses {
		if work, claimed, err := store.Claim(integration.WithTestOperation(t, t.Context()), class, idleClaimOwner, idleClaimLease); err != nil || claimed {
			t.Fatalf("empty %s claim = %+v, claimed %t, error %v, want no claim and no error", class, work, claimed, err)
		}
	}
	expected := scheduleEveryClass(t, stores, store)
	for _, class := range idleClaimClasses {
		work, claimed, err := store.Claim(integration.WithTestOperation(t, t.Context()), class, idleClaimOwner, idleClaimLease)
		if err != nil || !claimed {
			t.Fatalf("%s claim after scheduling = claimed %t, error %v, want a claimed item", class, claimed, err)
		}
		if !slices.Contains(expected[class], work.NodeID) || work.Class != class {
			t.Fatalf("%s claim leased node %s of class %s, want one of %v", class, work.NodeID, work.Class, expected[class])
		}
	}
}

// scheduleEveryClass schedules work through the public stores and returns
// the node IDs that a claim of each class may lease.
//   - Writing two nodes in one organization schedules live work for both.
//   - Adding a relationship between those nodes schedules access work for
//     both.
//   - Deleting a third node schedules cleanup work for that node.
//   - Adding a type key to the node type's CanContain list requests a rescan
//     of the organization. Rescan work uses [uuid.Nil] as its node ID.
//   - Beginning an access rollout for the organization schedules rollout
//     work with [uuid.Nil] as its node ID.
//   - [scheduleCopyAndRebuild] schedules rebuild work with [uuid.Nil] as its
//     node ID and copy work with the node ID it returns.
func scheduleEveryClass(t *testing.T, stores *fdbadapter.Stores, store *fdbadapter.SearchWorkStore) map[searchdomain.WorkClass][]uuid.UUID {
	t.Helper()
	source := putIdleClaimNode(t, stores, uuid.Must(uuid.NewV7()))
	target := putIdleClaimNode(t, stores, source.OrgID)
	relationship := &node.Relationship{OrgID: source.OrgID, SourceID: source.NodeID, RelationType: idleClaimKey("r"), TargetID: target.NodeID, CreatedBy: uuid.Nil, CreatedAt: clock.Now().UTC(), Props: nil}
	if err := stores.Relationships.Add(integration.WithTestOperation(t, t.Context()), relationship); err != nil {
		t.Fatalf("add relationship: %v", err)
	}
	deleted := putIdleClaimNode(t, stores, source.OrgID)
	if err := stores.Nodes.Delete(integration.WithTestOperation(t, t.Context()), deleted.OrgID, deleted.NodeID); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	kind, err := stores.NodeTypes.TypeByKey(integration.WithTestOperation(t, t.Context()), source.OrgID, source.TypeKey)
	if err != nil || kind == nil {
		t.Fatalf("read fixture node type: %v", err)
	}
	kind.CanContain = append(kind.CanContain, idleClaimKey("c"))
	if err := stores.NodeTypes.Set(integration.WithTestOperation(t, t.Context()), kind); err != nil {
		t.Fatalf("change fixture containment: %v", err)
	}
	beginIdleClaimRollout(t, stores, source.OrgID)
	copied := scheduleCopyAndRebuild(t, stores, store, source)
	pair := []uuid.UUID{source.NodeID, target.NodeID}
	return map[searchdomain.WorkClass][]uuid.UUID{
		searchdomain.WorkClassLive: pair, searchdomain.WorkClassAccess: pair,
		searchdomain.WorkClassCleanup: {deleted.NodeID}, searchdomain.WorkClassRescan: {uuid.Nil},
		searchdomain.WorkClassRollout: {uuid.Nil}, searchdomain.WorkClassRebuild: {uuid.Nil},
		searchdomain.WorkClassCopy: {copied},
	}
}

func beginIdleClaimRollout(t *testing.T, stores *fdbadapter.Stores, authorityID uuid.UUID) {
	t.Helper()
	rollouts := stores.SearchRollouts(clock.Wall{}, stores.SearchPolicySet())
	current, err := rollouts.Current(integration.WithTestOperation(t, t.Context()), authorityID)
	if err != nil {
		t.Fatalf("read access rollout: %v", err)
	}
	request := searchdomain.BeginAccessRollout{AuthorityID: authorityID, CandidateVersion: searchaccess.RotatedVersion, ExpectedGeneration: current.Generation}
	if _, err := rollouts.Begin(integration.WithTestOperation(t, t.Context()), request); err != nil {
		t.Fatalf("begin access rollout: %v", err)
	}
}
