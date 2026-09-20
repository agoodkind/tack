package foundationdb

import (
	"fmt"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
)

// Open opens a connection to FoundationDB using the given cluster file.
// API version is pinned to 740 (FDB 7.4.x).
// Call once at process startup; the returned DB is safe for concurrent use.
//
// transactionTimeout bounds every transaction this database creates, retries
// included: at API version 740 the binding does not reset the option after a
// retryable error, so one value bounds the whole retry loop. A zero or
// negative value sets no option (TACK-408).
//
// The binding keeps one Database per cluster file for the whole process. The
// timeout therefore reaches every holder of that cluster file, and the last
// Open of a given file decides it. Each process opens once and names its own
// value.
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
