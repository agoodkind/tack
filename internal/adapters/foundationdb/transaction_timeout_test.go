package foundationdb

import (
	"errors"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/testenv"
)

// testTransactionTimeout sets the store timeout for tests in this package.
// The original timeout test matched this value to the deployed default to
// exercise the application's transaction bound.
const testTransactionTimeout = 5 * time.Second

// fdbErrorTransactionTimedOut is the FoundationDB error code for a
// transaction that ran past its timeout.
const fdbErrorTransactionTimedOut = 1031

// TestOpenBoundsATransactionByTheTimeoutItWasGiven requires FoundationDB to
// refuse a transaction that remains open past its supplied timeout. The test
// does not depend on a caller deadline, which the request path does not
// always set (TACK-408).
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
// first read establishes the transaction's read version. The timeout
// controls whether the second read succeeds.
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
