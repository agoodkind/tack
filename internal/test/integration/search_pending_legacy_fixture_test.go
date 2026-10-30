package integration_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	pendingFixtureFamily = "search_pending_write"
	pendingFixtureLimit  = 8
)

// pendingFixtureRecord matches the deployed pending-write payload.
type pendingFixtureRecord struct {
	DocumentID  string    `json:"document_id"`
	Index       string    `json:"index"`
	Ordinal     uint64    `json:"ordinal"`
	Generation  int64     `json:"generation"`
	AbandonedAt time.Time `json:"abandoned_at"`
	Deadline    time.Time `json:"deadline"`
}

type pendingFixtureState struct {
	key    fdb.Key
	record pendingFixtureRecord
}

func pendingFixtureKey(t *testing.T, class searchdomain.WorkClass, orgID, nodeID uuid.UUID, suffix ...string) fdb.Key {
	t.Helper()
	prefix := bytes.Clone(fdbadapter.TestPrefixRange())
	if len(prefix) == 0 {
		t.Fatal("pending-write fixtures require an isolated test prefix")
	}
	elements := tuple.Tuple{pendingFixtureFamily, string(class), orgID.String(), nodeID.String()}
	for _, element := range suffix {
		elements = append(elements, element)
	}
	return fdb.Key(append(prefix, elements.Pack()...))
}

func pendingFixtureDatabase(t *testing.T) fdb.Database {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), integration.StoreTransactionTimeout)
	if err != nil {
		t.Fatalf("open pending-write fixture database: %v", err)
	}
	return database
}

func pendingFixtureTransaction(t *testing.T) fdb.Transaction {
	t.Helper()
	transaction, err := pendingFixtureDatabase(t).CreateTransaction()
	if err != nil {
		t.Fatalf("create pending-write fixture transaction: %v", err)
	}
	if err := transaction.Options().SetTimeout(int64(integration.StoreTransactionTimeout / time.Millisecond)); err != nil {
		t.Fatalf("set pending-write fixture timeout: %v", err)
	}
	return transaction
}

// nodePendingStates reads only one class/node prefix in the isolated test database.
func nodePendingStates(t *testing.T, env *migrationEnv, nodeID uuid.UUID, class searchdomain.WorkClass) []pendingFixtureState {
	t.Helper()
	keyRange, err := fdb.PrefixRange(pendingFixtureKey(t, class, env.OrgID, nodeID))
	if err != nil {
		t.Fatalf("create pending-write fixture range for node %s: %v", nodeID, err)
	}
	transaction := pendingFixtureTransaction(t)
	defer transaction.Cancel()
	entries, err := transaction.GetRange(keyRange, fdb.RangeOptions{Limit: pendingFixtureLimit}).GetSliceWithError()
	if err != nil {
		t.Fatalf("read pending-write fixture records of node %s: %v", nodeID, err)
	}
	states := make([]pendingFixtureState, 0, len(entries))
	for _, entry := range entries {
		var record pendingFixtureRecord
		if err := json.Unmarshal(entry.Value, &record); err != nil {
			t.Fatalf("decode pending-write fixture record %x: %v", entry.Key, err)
		}
		states = append(states, pendingFixtureState{key: entry.Key, record: record})
	}
	return states
}

func singleNodePending(t *testing.T, env *migrationEnv, nodeID uuid.UUID) pendingFixtureState {
	t.Helper()
	states := nodePendingStates(t, env, nodeID, searchdomain.WorkClassLive)
	if len(states) != 1 {
		t.Fatalf("node %s has %d live pending records, want one", nodeID, len(states))
	}
	return states[0]
}

// seedLegacyPending replaces the real abandoned record with the deployed per-node key.
func seedLegacyPending(t *testing.T, env *migrationEnv, nodeID uuid.UUID, original pendingFixtureState, existing *pendingFixtureRecord) {
	t.Helper()
	transaction := pendingFixtureTransaction(t)
	defer transaction.Cancel()
	transaction.Clear(original.key)
	writePendingFixture(t, transaction, pendingFixtureKey(t, searchdomain.WorkClassLive, env.OrgID, nodeID), original.record)
	if existing != nil {
		key := pendingFixtureKey(t, searchdomain.WorkClassLive, env.OrgID, nodeID, existing.DocumentID, existing.Index)
		writePendingFixture(t, transaction, key, *existing)
	}
	if err := transaction.Commit().Get(); err != nil {
		t.Fatalf("commit deployed pending-write state of node %s: %v", nodeID, err)
	}
}

func writePendingFixture(t *testing.T, transaction fdb.Transaction, key fdb.Key, record pendingFixtureRecord) {
	t.Helper()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("encode pending-write fixture record %s: %v", record.DocumentID, err)
	}
	transaction.Set(key, encoded)
}

func requirePendingFixture(t *testing.T, key fdb.Key, expected pendingFixtureRecord) {
	t.Helper()
	transaction := pendingFixtureTransaction(t)
	defer transaction.Cancel()
	encoded, err := transaction.Get(key).Get()
	if err != nil {
		t.Fatalf("read preserved pending-write record %s: %v", expected.DocumentID, err)
	}
	var actual pendingFixtureRecord
	if err := json.Unmarshal(encoded, &actual); err != nil {
		t.Fatalf("decode preserved pending-write record %s: %v", expected.DocumentID, err)
	}
	if actual != expected {
		t.Fatalf("preserved pending write = %+v, want %+v", actual, expected)
	}
}

func requireLegacyPendingRemoved(t *testing.T, env *migrationEnv, nodeID uuid.UUID) {
	t.Helper()
	transaction := pendingFixtureTransaction(t)
	defer transaction.Cancel()
	encoded, err := transaction.Get(pendingFixtureKey(t, searchdomain.WorkClassLive, env.OrgID, nodeID)).Get()
	if err != nil || len(encoded) != 0 {
		t.Fatalf("deployed pending key of node %s retains %d bytes, error %v, want an adopted record", nodeID, len(encoded), err)
	}
}
