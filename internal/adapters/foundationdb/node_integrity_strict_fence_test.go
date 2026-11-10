package foundationdb_test

import (
	"errors"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/testenv"
)

const (
	strictTestTransactionTimeout = 5 * time.Second
	// fdbErrorNotCommitted is the FoundationDB error code for a transaction
	// conflict with a write that another transaction commits to a key the
	// transaction read after its read version.
	fdbErrorNotCommitted = 1020
)

// TestNodeIntegrityStrictActivationConflictsWithInFlightMutation calls
// FenceNodeMutation in a mutation transaction while strict validation is
// inactive. Activation commits before the mutation transaction attempts to
// commit. The test checks that the mutation commit fails with a conflict and
// that the retried transaction reads the activation record. It also checks
// that the retried commit increments the write fence bucket epoch exactly once.
func TestNodeIntegrityStrictActivationConflictsWithInFlightMutation(t *testing.T) {
	database, err := fdbadapter.Open(testenv.FoundationDB(t), strictTestTransactionTimeout)
	if err != nil {
		t.Fatalf("open fdb: %v", err)
	}
	prefixID := uuid.New()
	prefix := append([]byte("tack-test:"), prefixID[:]...)
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() {
		clearStrictTestPrefix(t, database, prefix)
		fdbadapter.SetTestPrefix(nil)
	})
	store := fdbadapter.NewNodeStore(database, clock.Wall{})
	ctx := t.Context()
	orgID, nodeID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	bucket := node.WriteFenceBucket(orgID, nodeID)
	evidence, err := store.CaptureStrictEvidence(ctx)
	if err != nil {
		t.Fatalf("capture the evidence: %v", err)
	}

	inFlight, err := database.CreateTransaction()
	if err != nil {
		t.Fatalf("create the mutation transaction: %v", err)
	}
	active, err := fdbadapter.FenceNodeMutation(ctx, inFlight, orgID, nodeID)
	if err != nil || active {
		t.Fatalf("fence before activation = %t, %v; want inactive", active, err)
	}
	activation := node.StrictActivation{Active: true, ActivatedAt: clock.Now().UTC(), ScanID: uuid.Must(uuid.NewV7()), RecordsValidated: 0}
	if _, err := store.ActivateStrict(ctx, evidence, activation); err != nil {
		t.Fatalf("activate strict validation: %v", err)
	}
	commitErr := inFlight.Commit().Get()
	var conflict fdb.Error
	if !errors.As(commitErr, &conflict) || conflict.Code != fdbErrorNotCommitted {
		t.Fatalf("in-flight commit after activation = %v, want FoundationDB error %d", commitErr, fdbErrorNotCommitted)
	}

	if err := inFlight.OnError(conflict).Get(); err != nil {
		t.Fatalf("reset the mutation transaction for a retry: %v", err)
	}
	active, err = fdbadapter.FenceNodeMutation(ctx, inFlight, orgID, nodeID)
	if err != nil || !active {
		t.Fatalf("fence in the retried attempt = %t, %v; want active", active, err)
	}
	if err := inFlight.Commit().Get(); err != nil {
		t.Fatalf("commit the retried attempt: %v", err)
	}
	later, err := store.CaptureStrictEvidence(ctx)
	if err != nil {
		t.Fatalf("capture the evidence after the retry: %v", err)
	}
	if later.Buckets[bucket] != evidence.Buckets[bucket]+1 {
		t.Fatalf("bucket %d epoch = %d, want %d", bucket, later.Buckets[bucket], evidence.Buckets[bucket]+1)
	}
}

func clearStrictTestPrefix(t *testing.T, database fdb.Database, prefix []byte) {
	t.Helper()
	end := append(append([]byte(nil), prefix...), 0xFF)
	_, err := database.Transact(func(tr fdb.Transaction) (any, error) {
		tr.ClearRange(fdb.KeyRange{Begin: fdb.Key(prefix), End: fdb.Key(end)})
		return nil, nil
	})
	if err != nil {
		t.Logf("cleanup: clear range: %v", err)
	}
}
