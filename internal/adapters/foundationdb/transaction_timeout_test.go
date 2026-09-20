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
// still open past the timeout is refused, rather than waiting for a caller
// deadline the request path does not always set (TACK-408).
func TestOpenBoundsATransactionByTheTimeoutItWasGiven(t *testing.T) {
	const shortTimeout = 300 * time.Millisecond
	database, err := Open(testenv.FoundationDB(t), shortTimeout)
	if err != nil {
		t.Fatalf("Open with %s: %v", shortTimeout, err)
	}

	err = readAfterWaiting(database, shortTimeout*3)
	if err == nil {
		t.Fatalf("a read %s into a %s bound succeeded, want the transaction refused past the bound",
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
