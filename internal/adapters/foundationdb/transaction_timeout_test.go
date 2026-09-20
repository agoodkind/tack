package foundationdb

import (
	"errors"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/testenv"
)

// testTransactionTimeout is what the tests in this package open the store
// with. It matches the deployed default so a test exercises the same bound the
// app runs under.
const testTransactionTimeout = 5 * time.Second

// fdbErrorTransactionTimedOut is the FoundationDB error code for a transaction
// that ran past its timeout.
const fdbErrorTransactionTimedOut = 1031

// TestOpenBoundsATransactionByItsTimeout proves the bound Open sets is real on
// a running cluster: a transaction still open past the timeout is refused,
// rather than waiting for a caller deadline the request path does not always
// set (TACK-408).
func TestOpenBoundsATransactionByItsTimeout(t *testing.T) {
	const timeout = 300 * time.Millisecond
	database, err := Open(testenv.FoundationDB(t), timeout)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	transaction, err := database.CreateTransaction()
	if err != nil {
		t.Fatalf("CreateTransaction: %v", err)
	}
	// One read first, so the transaction already has a read version and the
	// second read reaches the bound rather than the read version request.
	if _, err := transaction.Get(fdb.Key("tack-test:timeout-probe")).Get(); err != nil {
		t.Fatalf("first read: %v", err)
	}

	time.Sleep(timeout * 3)

	started := time.Now()
	_, err = transaction.Get(fdb.Key("tack-test:timeout-probe")).Get()
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("the read after the timeout succeeded; the database option set no bound")
	}
	var fdbErr fdb.Error
	if !errors.As(err, &fdbErr) {
		t.Fatalf("the read after the timeout returned %T (%v), want an fdb.Error", err, err)
	}
	if fdbErr.Code != fdbErrorTransactionTimedOut {
		t.Fatalf("the read after the timeout returned FoundationDB error %d (%v), want %d",
			fdbErr.Code, err, fdbErrorTransactionTimedOut)
	}
	if elapsed > timeout {
		t.Fatalf("the refusal took %s, longer than the %s bound", elapsed, timeout)
	}
}

// TestOpenLeavesATransactionUnboundedWithoutATimeout proves the zero value is
// the old behavior, so an environment that renders no timeout keeps running
// rather than refusing every transaction.
func TestOpenLeavesATransactionUnboundedWithoutATimeout(t *testing.T) {
	database, err := Open(testenv.FoundationDB(t), 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	transaction, err := database.CreateTransaction()
	if err != nil {
		t.Fatalf("CreateTransaction: %v", err)
	}
	if _, err := transaction.Get(fdb.Key("tack-test:unbounded-probe")).Get(); err != nil {
		t.Fatalf("first read: %v", err)
	}

	time.Sleep(time.Second)

	if _, err := transaction.Get(fdb.Key("tack-test:unbounded-probe")).Get(); err != nil {
		t.Fatalf("the read a second after the first returned %v, want the transaction still usable", err)
	}
}
