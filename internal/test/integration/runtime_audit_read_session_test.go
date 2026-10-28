package integration_test

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"goodkind.io/tack/internal/audit"
)

const (
	// sessionCleanupReceipts is the number of published receipts the cleanup test stores.
	sessionCleanupReceipts = 8
	// sessionCleanupBuckets is the number of buckets above the recovery buckets
	// that the cleanup test chooses from. The recovery loops of other tests
	// never clean those buckets.
	sessionCleanupBuckets = 1000
)

// lifecycleResult is the result of one read lifecycle call in a goroutine.
type lifecycleResult struct {
	count int
	err   error
}

// awaitAfterLock waits up to sessionLockWait for result before it commits
// transaction. After the commit, it waits for result. It returns the result
// and whether the call had not finished when the commit started.
func awaitAfterLock(t *testing.T, transaction pgx.Tx, result <-chan lifecycleResult) (lifecycleResult, bool) {
	t.Helper()
	timer := time.NewTimer(sessionLockWait)
	defer timer.Stop()
	select {
	case finished := <-result:
		if err := transaction.Commit(t.Context()); err != nil {
			t.Fatalf("commit the lock transaction: %v", err)
		}
		return finished, false
	case <-timer.C:
	}
	if err := transaction.Commit(t.Context()); err != nil {
		t.Fatalf("commit the lock transaction: %v", err)
	}
	return <-result, true
}

// TestRuntimeAuditReadCleanupSkipsLockedReceipt stores published receipts in
// one bucket and locks one of them in another transaction, as a concurrent
// cleanup in another application instance does. CleanupPublished must delete
// the other receipts without waiting for the lock. After the lock transaction
// commits, the application connection must still run as the application
// login and must validate a token. A second cleanup must delete the
// remaining receipt (TACK-583).
func TestRuntimeAuditReadCleanupSkipsLockedReceipt(t *testing.T) {
	fixture := newSessionRoleFixture(t)
	bucket := uint16(audit.ReadRecoveryBuckets + rand.IntN(sessionCleanupBuckets))
	receipts := make([]uuid.UUID, sessionCleanupReceipts)
	for index := range receipts {
		receipts[index] = uuid.Must(uuid.NewV7())
	}
	insert := readTestSQL(t, "runtime_audit_read_session_receipts.sql")
	if _, err := fixture.admin.Exec(t.Context(), insert, receipts, int(bucket)); err != nil {
		t.Fatalf("store the published receipts: %v", err)
	}
	fixture.requireApplicationSession(t, "before cleanup")
	transaction := lockReadState(t, fixture.admin, receipts[0])
	result := make(chan lifecycleResult, 1)
	go func() {
		deleted, err := fixture.store.CleanupPublished(t.Context(), bucket, audit.ReadCleanupBatch)
		result <- lifecycleResult{count: deleted, err: err}
	}()
	cleaned, waited := awaitAfterLock(t, transaction, result)
	t.Logf("cleanup deleted %d receipts, waited for the lock = %t, error = %v", cleaned.count, waited, cleaned.err)
	fixture.requireApplicationSession(t, "after cleanup")
	if waited {
		t.Fatalf("cleanup waited %s for the locked receipt %s", sessionLockWait, receipts[0])
	}
	if cleaned.err != nil || cleaned.count != sessionCleanupReceipts-1 {
		t.Fatalf("cleanup = %d, %v; want %d receipts and nil", cleaned.count, cleaned.err, sessionCleanupReceipts-1)
	}
	remaining, err := fixture.store.CleanupPublished(t.Context(), bucket, audit.ReadCleanupBatch)
	if err != nil || remaining != 1 {
		t.Fatalf("second cleanup = %d, %v; want the receipt %s and nil", remaining, err, receipts[0])
	}
}

// TestRuntimeAuditReadRecoveryRetryKeepsSessionRole admits a read through the
// application login, expires its lease, and locks its pending row in another
// transaction. RecoverExpired waits for the lock. The lock transaction then
// commits a write to the row to attempt to trigger an internal YugabyteDB
// retry of the ops_read_recover statement. In CI run 40628475074, the
// connection that ran the statement reported ops_outbox_owner as
// current_user after the commit (TACK-583). RecoverExpired must report one
// recovered read with a nil error, and the read must have an incomplete
// receipt. The next application connection must run as the application
// login and must validate a token. The test logs how many connections the
// one-connection pool opened during recovery. A new connection shows that
// the read lifecycle closed a connection with a changed role. The test does
// not require one, because the internal retry does not happen on every run.
func TestRuntimeAuditReadRecoveryRetryKeepsSessionRole(t *testing.T) {
	fixture := newSessionRoleFixture(t)
	read := newReadFixture(t)
	read.begin(t, fixture.store)
	eventID := read.admission.Token.EventID
	expire := readTestSQL(t, "runtime_audit_read_expire_lease.sql")
	if _, err := fixture.admin.Exec(t.Context(), expire, []uuid.UUID{eventID}); err != nil {
		t.Fatalf("expire the read lease: %v", err)
	}
	fixture.requireApplicationSession(t, "before recovery")
	connectionsBefore := fixture.application.Stat().NewConnsCount()
	transaction := lockReadState(t, fixture.admin, eventID)
	result := make(chan lifecycleResult, 1)
	go func() {
		recovered, err := fixture.store.RecoverExpired(t.Context(), audit.ReadBucket(eventID), audit.ReadRecoveryScanLimit)
		result <- lifecycleResult{count: recovered, err: err}
	}()
	recovery, waited := awaitAfterLock(t, transaction, result)
	t.Logf("recovery saved %d reads, waited for the lock = %t, error = %v", recovery.count, waited, recovery.err)
	fixture.requireApplicationSession(t, "after recovery")
	t.Logf("the application pool opened %d connections during recovery",
		fixture.application.Stat().NewConnsCount()-connectionsBefore)
	if recovery.err != nil || recovery.count != 1 {
		t.Fatalf("recover the expired read = %d, %v; want 1 and nil", recovery.count, recovery.err)
	}
	var incomplete, rows int
	state := readTestSQL(t, "runtime_audit_read_session_state.sql")
	if err := fixture.admin.QueryRow(t.Context(), state, []uuid.UUID{eventID}).Scan(&incomplete, &rows); err != nil {
		t.Fatalf("read the receipt of read %s: %v", eventID, err)
	}
	if incomplete != 1 || rows != 1 {
		t.Fatalf("read %s has %d incomplete receipts in %d rows, want 1 and 1", eventID, incomplete, rows)
	}
}
