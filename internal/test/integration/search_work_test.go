package integration

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/testenv"
)

// newWorkStore records a serving index name and returns the work store. The
// store tests never contact OpenSearch.
func newWorkStore(t *testing.T, stores *fdbadapter.Stores) *fdbadapter.SearchWorkStore {
	t.Helper()
	if err := stores.InitializeSearchIndex(t.Context(), "work-store-"+uuid.Must(uuid.NewV7()).String()); err != nil {
		t.Fatalf("record serving index: %v", err)
	}
	return stores.SearchWork(clock.Wall{})
}

// claimAll claims every claimable item of class for one minute and returns
// the items by node.
func claimAll(t *testing.T, store *fdbadapter.SearchWorkStore, class searchdomain.WorkClass) map[uuid.UUID]searchdomain.Work {
	t.Helper()
	return claimAllFor(t, store, class, time.Minute)
}

// claimAllFor claims every claimable item of class under lease and returns
// the items by node.
func claimAllFor(t *testing.T, store *fdbadapter.SearchWorkStore, class searchdomain.WorkClass, lease time.Duration) map[uuid.UUID]searchdomain.Work {
	t.Helper()
	claimed := map[uuid.UUID]searchdomain.Work{}
	for {
		work, err := store.Claim(t.Context(), class, "inspector-"+uuid.NewString(), lease)
		if errors.Is(err, searchdomain.ErrNoWork) {
			return claimed
		}
		if err != nil {
			t.Fatalf("claim %s work: %v", class, err)
		}
		claimed[work.NodeID] = work
	}
}

func TestSearchWorkScheduledInCreateTransaction(t *testing.T) {
	stores := newSearchStore(t)
	store := newWorkStore(t, stores)
	fixture := putSearchText(t, stores, "created text", "excluded text")
	now := clock.Now().UTC()
	idempotency := &node.IdempotencyRecord{Key: "create-once", NodeID: uuid.Must(uuid.NewV7()), Fingerprint: "", CreatedAt: now, Source: ""}
	created := &node.Node{ID: idempotency.NodeID, OrgID: fixture.OrgID, NodeType: fixture.TypeKey, Name: "created", CreatedAt: now, UpdatedAt: now}
	if err := stores.Nodes.CreateAtomic(t.Context(), created, nil, nil, nil, nil, idempotency); err != nil {
		t.Fatalf("create node: %v", err)
	}
	rejected := &node.Node{ID: uuid.Must(uuid.NewV7()), OrgID: fixture.OrgID, NodeType: fixture.TypeKey, Name: "rejected", CreatedAt: now, UpdatedAt: now}
	if err := stores.Nodes.CreateAtomic(t.Context(), rejected, nil, nil, nil, nil, idempotency); err == nil {
		t.Fatal("create with a reused idempotency key succeeded")
	}
	live := claimAll(t, store, searchdomain.WorkClassLive)
	work, exists := live[created.ID]
	if !exists || work.Generation != 1 || work.Revision != "1" || work.Target == "" {
		t.Fatalf("created node work = %+v, want generation and revision 1 on the serving index", work)
	}
	if _, exists := live[rejected.ID]; exists {
		t.Fatal("the rolled-back create scheduled search work")
	}
}

func TestSearchWorkScheduledInEditTransaction(t *testing.T) {
	stores := newSearchStore(t)
	store := newWorkStore(t, stores)
	fixture := putSearchText(t, stores, "original text", "excluded text")
	before := claimAll(t, store, searchdomain.WorkClassLive)[fixture.NodeID]
	writeSearchNode(t, stores, fixture, "edited text", "excluded text")
	if err := store.Yield(t.Context(), before); !errors.Is(err, searchdomain.ErrWorkChanged) {
		t.Fatalf("stale claim yield = %v, want changed work", err)
	}
	after := claimAll(t, store, searchdomain.WorkClassLive)[fixture.NodeID]
	if after.Generation != before.Generation+1 || after.Revision == before.Revision || after.Cursor != "" {
		t.Fatalf("edited work = %+v, want the next generation, a new revision, and no cursor", after)
	}
}

func TestSearchWorkScheduledInRelationshipTransaction(t *testing.T) {
	stores := newSearchStore(t)
	store := newWorkStore(t, stores)
	source := putSearchText(t, stores, "source text", "excluded")
	target := putSearchTextInOrg(t, stores, source.OrgID, "target text", "excluded")
	relationship := &node.Relationship{OrgID: source.OrgID, SourceID: source.NodeID, RelationType: opaqueSearchKey("r"), TargetID: target.NodeID, CreatedBy: uuid.Nil, CreatedAt: clock.Now().UTC(), Props: nil}
	if err := stores.Relationships.Add(t.Context(), relationship); err != nil {
		t.Fatalf("add relationship: %v", err)
	}
	access := claimAll(t, store, searchdomain.WorkClassAccess)
	live := claimAll(t, store, searchdomain.WorkClassLive)
	for _, nodeID := range []uuid.UUID{source.NodeID, target.NodeID} {
		accessWork, accessExists := access[nodeID]
		liveWork, liveExists := live[nodeID]
		if !accessExists || !liveExists || accessWork.Generation != 2 || liveWork.Generation != 2 || liveWork.Revision != "1" {
			t.Fatalf("node %s access %+v live %+v, want both at generation 2 with content revision 1", nodeID, accessWork, liveWork)
		}
	}
}

// TestSearchRescanRequestedByContainmentChange requires a write that adds a
// type key to the CanContain list of a node type to request a rescan of the
// organization.
func TestSearchRescanRequestedByContainmentChange(t *testing.T) {
	stores := newSearchStore(t)
	store := newWorkStore(t, stores)
	fixture := putSearchText(t, stores, "contained text", "excluded")
	claimAll(t, store, searchdomain.WorkClassRescan)
	kind, err := stores.NodeTypes.TypeByKey(t.Context(), fixture.OrgID, fixture.TypeKey)
	if err != nil || kind == nil {
		t.Fatalf("read fixture node type: %v", err)
	}
	kind.CanContain = append(kind.CanContain, opaqueSearchKey("c"))
	if err := stores.NodeTypes.Set(t.Context(), kind); err != nil {
		t.Fatalf("change fixture containment: %v", err)
	}
	if _, exists := claimAll(t, store, searchdomain.WorkClassRescan)[uuid.Nil]; !exists {
		t.Fatal("a CanContain change requested no rescan")
	}
}

// TestSearchWorkOffWithoutEndpoint requires FoundationDB to store no search
// generation or search access record for a project that MCP creates on a
// graph without OPENSEARCH_ENDPOINT.
func TestSearchWorkOffWithoutEndpoint(t *testing.T) {
	t.Setenv("OPENSEARCH_ENDPOINT", "")
	harness := NewMCPHarness(t)
	created := harness.Call(t, "tack_create_project", datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, Name: "search off project",
		Properties: datagen.NodeProperties{"identifier": json.RawMessage(strconv.Quote("OFF" + strconv.FormatInt(clock.Now().UnixNano()%1_000_000, 10)))},
	})
	database, err := fdbadapter.Open(testenv.FoundationDB(t), testTransactionTimeout)
	if err != nil {
		t.Fatalf("open foundationdb: %v", err)
	}
	for _, family := range []string{"search_generation", "search_access"} {
		key := fdb.Key(tuple.Tuple{family, harness.orgID.String(), created.RawID()}.Pack())
		value, err := database.ReadTransact(func(tr fdb.ReadTransaction) (any, error) { return tr.Get(key).Get() })
		if err != nil {
			t.Fatalf("read %s key: %v", family, err)
		}
		if stored, _ := value.([]byte); len(stored) != 0 {
			t.Fatalf("project %s has a %s record with search off", created.RawID(), family)
		}
	}
}

func TestSearchWorkScheduledInDeleteTransaction(t *testing.T) {
	stores := newSearchStore(t)
	store := newWorkStore(t, stores)
	fixture := putSearchText(t, stores, "deleted text", "excluded")
	if err := stores.Nodes.Delete(t.Context(), fixture.OrgID, fixture.NodeID); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	if _, exists := claimAll(t, store, searchdomain.WorkClassLive)[fixture.NodeID]; exists {
		t.Fatal("deleted node still has live work")
	}
	cleanup, exists := claimAll(t, store, searchdomain.WorkClassCleanup)[fixture.NodeID]
	if !exists || !cleanup.Deleted || cleanup.Generation != 2 {
		t.Fatalf("deletion cleanup = %+v, want deleted work at generation 2", cleanup)
	}
}

// TestSearchWorkReleaseWaitsRetryAfter requires released work to stay
// unclaimable for its failure's RetryAfter when RetryAfter exceeds the
// store's default retry delay.
func TestSearchWorkReleaseWaitsRetryAfter(t *testing.T) {
	stores := newSearchStore(t)
	store := newWorkStore(t, stores)
	fixture := putSearchText(t, stores, "retry text", "excluded")
	work := claimAll(t, store, searchdomain.WorkClassLive)[fixture.NodeID]
	const retryAfter = 8 * time.Second
	released := clock.Now()
	failure := searchdomain.Failure{Message: "engine timeout", Counted: false, RetryAfter: retryAfter}
	if err := store.Release(t.Context(), work, failure); err != nil {
		t.Fatalf("release work: %v", err)
	}
	waitUntil(t, released.Add(6*time.Second))
	if _, exists := claimAll(t, store, searchdomain.WorkClassLive)[fixture.NodeID]; exists {
		t.Fatalf("released work was claimable 6 s after release, want a wait of %s", retryAfter)
	}
	waitUntil(t, released.Add(retryAfter))
	if _, exists := claimAll(t, store, searchdomain.WorkClassLive)[fixture.NodeID]; !exists {
		t.Fatalf("released work was not claimable %s after release", retryAfter)
	}
}
