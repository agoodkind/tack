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

// TestOpenBoundsATransactionByTheTimeoutItWasGiven asserts that a transaction
// open past the timeout is refused, rather than waiting for a caller deadline
// the request path does not always set, and that the bound is the value the
// last Open was given.
//
// Both assertions run in one test against one cluster file. The binding keeps
// one Database per cluster file for the whole process (the openDatabases map
// in fdb.go), so separate tests would be separate orderings of the same
// handle. The cleanup restores the package's timeout for whatever runs next.
func TestOpenBoundsATransactionByTheTimeoutItWasGiven(t *testing.T) {
	clusterFile := testenv.FoundationDB(t)
	t.Cleanup(func() {
		if _, err := Open(clusterFile, testTransactionTimeout); err != nil {
			t.Errorf("restore the package timeout: %v", err)
		}
	})

	const generousTimeout = 4 * time.Second
	generous, err := Open(clusterFile, generousTimeout)
	if err != nil {
		t.Fatalf("Open with %s: %v", generousTimeout, err)
	}
	if err := readAfterWaiting(generous, time.Second); err != nil {
		t.Fatalf("a read one second into a %s bound returned %v, want the transaction still usable",
			generousTimeout, err)
	}

	const shortTimeout = 300 * time.Millisecond
	short, err := Open(clusterFile, shortTimeout)
	if err != nil {
		t.Fatalf("Open with %s: %v", shortTimeout, err)
	}
	err = readAfterWaiting(short, shortTimeout*3)
	if err == nil {
		t.Fatalf("a read %s into a %s bound succeeded; the shorter timeout replaced nothing",
			shortTimeout*3, shortTimeout)
	}
	var fdbErr fdb.Error
	if !errors.As(err, &fdbErr) {
		t.Fatalf("the read after the timeout returned %T (%v), want an fdb.Error", err, err)
	}
	if fdbErr.Code != fdbErrorTransactionTimedOut {
		t.Fatalf("the read after the timeout returned FoundationDB error %d (%v), want %d",
			fdbErr.Code, err, fdbErrorTransactionTimedOut)
	}
}

// readAfterWaiting opens a transaction, reads, waits, and reads again. The
// first read establishes the transaction's read version. The bound decides the
// second read.
func readAfterWaiting(database fdb.Database, wait time.Duration) error {
	transaction, err := database.CreateTransaction()
	if err != nil {
		return err
	}
	probe := fdb.Key("tack-test:timeout-probe")
	if _, err := transaction.Get(probe).Get(); err != nil {
		return err
	}
	time.Sleep(wait)
	_, err = transaction.Get(probe).Get()
	return err
}
