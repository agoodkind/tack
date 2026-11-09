package foundationdb

import (
	"fmt"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
)

// Open opens a connection to FoundationDB using the given cluster file. It
// selects API version 740 (FoundationDB 7.4.x). Call Open once at process
// startup. The returned database supports concurrent use.
//
// transactionTimeout limits transactions that the database creates,
// including retries. At API version 740, the binding does not reset the
// timeout after a retryable error. Zero and negative values do not set the
// option (TACK-408). The returned database applies the timeout to every
// transaction it creates.
func Open(clusterFile string, transactionTimeout time.Duration) (fdb.Database, error) {
	if err := fdb.APIVersion(740); err != nil {
		return fdb.Database{}, fmt.Errorf("fdb api version: %w", err)
	}
	db, err := fdb.OpenDatabase(clusterFile)
	if err != nil {
		return fdb.Database{}, fmt.Errorf("fdb open: %w", err)
	}
	if transactionTimeout > 0 {
		if err := db.Options().SetTransactionTimeout(transactionTimeout.Milliseconds()); err != nil {
			return fdb.Database{}, fmt.Errorf("fdb set transaction timeout %s: %w", transactionTimeout, err)
		}
	}
	return db, nil
}
