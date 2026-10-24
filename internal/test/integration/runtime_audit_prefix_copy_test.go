package integration_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
)

const (
	// prefixCopyTimeout limits each FoundationDB transaction that reads or
	// restores the keys under the test prefix.
	prefixCopyTimeout = 5 * time.Second
	// outboxKeyFamily matches the operator outbox key family in
	// internal/adapters/foundationdb/keys.go.
	outboxKeyFamily = "ops_outbox"
)

// readTestKeys returns every key and value in keyRange.
func readTestKeys(t *testing.T, database fdb.Database, keyRange fdb.KeyRange) []fdb.KeyValue {
	t.Helper()
	copied, err := database.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		t.Fatalf("read the keys under the test prefix: %v", err)
	}
	keyValues, ok := copied.([]fdb.KeyValue)
	if !ok {
		t.Fatalf("read the keys under the test prefix: got %T, want []fdb.KeyValue", copied)
	}
	return keyValues
}

// restoreTestPrefix clears the active test prefix and writes saved back.
func restoreTestPrefix(t *testing.T, database fdb.Database, saved []fdb.KeyValue) {
	t.Helper()
	keyRange := testPrefixKeys(t)
	if _, err := database.Transact(func(tr fdb.Transaction) (any, error) {
		tr.ClearRange(keyRange)
		for _, keyValue := range saved {
			tr.Set(keyValue.Key, keyValue.Value)
		}
		return nil, nil
	}); err != nil {
		t.Fatalf("restore the keys under the test prefix: %v", err)
	}
}

// testPrefixKeys returns the range of keys under the active test prefix.
func testPrefixKeys(t *testing.T) fdb.KeyRange {
	t.Helper()
	return prefixKeyRange(t, activeTestPrefix(t))
}

// testOutboxKeys returns the range of operator outbox entries under the
// active test prefix.
func testOutboxKeys(t *testing.T) fdb.KeyRange {
	t.Helper()
	outboxPrefix := append(bytes.Clone(activeTestPrefix(t)), tuple.Tuple{outboxKeyFamily}.Pack()...)
	return prefixKeyRange(t, outboxPrefix)
}

// activeTestPrefix returns the FoundationDB test prefix of this process.
func activeTestPrefix(t *testing.T) []byte {
	t.Helper()
	prefix := fdbadapter.TestPrefixRange()
	if len(prefix) == 0 {
		t.Fatal("the test environment did not set a FoundationDB test prefix")
	}
	return prefix
}

// prefixKeyRange returns the range of keys that start with prefix.
func prefixKeyRange(t *testing.T, prefix []byte) fdb.KeyRange {
	t.Helper()
	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		t.Fatalf("create the range of prefix %x: %v", prefix, err)
	}
	return keyRange
}
