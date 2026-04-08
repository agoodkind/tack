//go:build fdb

package foundationdb

import (
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
)

// Open opens a connection to FoundationDB using the given cluster file.
// API version is pinned to 730 (FDB 7.3.x).
// Call once at process startup; the returned DB is safe for concurrent use.
func Open(clusterFile string) (fdb.Database, error) {
	if err := fdb.APIVersion(730); err != nil {
		return fdb.Database{}, fmt.Errorf("fdb api version: %w", err)
	}
	db, err := fdb.OpenDatabase(clusterFile)
	if err != nil {
		return fdb.Database{}, fmt.Errorf("fdb open: %w", err)
	}
	return db, nil
}
