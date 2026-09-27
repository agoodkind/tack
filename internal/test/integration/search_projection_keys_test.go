package integration

import (
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/testenv"
)

type projectionKeyValue struct {
	family string
	value  string
}

func snapshotProjectionKeys(t *testing.T) map[string]projectionKeyValue {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), testTransactionTimeout)
	if err != nil {
		t.Fatalf("open projection snapshot database: %v", err)
	}
	prefix := fdbadapter.TestPrefixRange()
	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		t.Fatalf("create projection snapshot range: %v", err)
	}
	transaction, err := database.CreateTransaction()
	if err != nil {
		t.Fatalf("create projection snapshot transaction: %v", err)
	}
	defer transaction.Cancel()
	entries, err := transaction.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		t.Fatalf("read projection snapshot: %v", err)
	}
	snapshot := make(map[string]projectionKeyValue, len(entries))
	for _, entry := range entries {
		parts, decodeErr := tuple.Unpack(entry.Key[len(prefix):])
		if decodeErr != nil || len(parts) == 0 {
			t.Fatalf("decode projection snapshot key: %v", decodeErr)
		}
		family, ok := parts[0].(string)
		if !ok {
			t.Fatalf("projection snapshot key family is not a string: %v", parts[0])
		}
		snapshot[string(entry.Key)] = projectionKeyValue{family: family, value: string(entry.Value)}
	}
	return snapshot
}

func withoutProjectionMetadata(snapshot map[string]projectionKeyValue) map[string]projectionKeyValue {
	other := make(map[string]projectionKeyValue)
	for key, value := range snapshot {
		if value.family == "property_def" || value.family == "ops_outbox" {
			continue
		}
		other[key] = value
	}
	return other
}
