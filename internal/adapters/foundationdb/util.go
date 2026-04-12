package foundationdb

import (
	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

// extractLastUUIDs unpacks keys and returns the UUID at position idx from each key.
// Used by all stores that need to extract IDs from range scan results.
func extractLastUUIDs(kvs []fdb.KeyValue, idx int) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(kvs))
	for _, kv := range kvs {
		t, err := tuple.Unpack(kv.Key)
		if err != nil || len(t) <= idx {
			continue
		}
		s, ok := t[idx].(string)
		if !ok {
			continue
		}
		id, err := uuid.Parse(s)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}
