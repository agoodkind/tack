package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

func removePrimaryParentFixture(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID) {
	t.Helper()
	current, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, nodeID)
	if err != nil || current == nil {
		t.Fatalf("read primary parent fixture %s: %v", nodeID, err)
	}

	delete(current.Props, "parent_id")
	encoded, err := json.Marshal(current)
	if err != nil {
		t.Fatalf("encode primary parent fixture %s: %v", nodeID, err)
	}

	database, err := fdbadapter.Open(testenv.FoundationDB(t), nodeAuditTransactionTimeout)
	if err != nil {
		t.Fatalf("open primary parent fixture store: %v", err)
	}

	_, err = database.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(
			parentMigrationKey(tuple.Tuple{
				"node_instance",
				env.OrgID.String(),
				current.NodeType,
				nodeID.String(),
			}),
			encoded,
		)

		return nil, nil
	})
	if err != nil {
		t.Fatalf("remove primary parent property from fixture %s: %v", nodeID, err)
	}
}

func removeParentResolutionFixture(t *testing.T, nodeID uuid.UUID) {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), nodeAuditTransactionTimeout)
	if err != nil {
		t.Fatalf("open parent resolution fixture store: %v", err)
	}

	_, err = database.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Clear(
			parentMigrationKey(tuple.Tuple{
				"node_resolve",
				nodeID.String(),
			}),
		)

		return nil, nil
	})
	if err != nil {
		t.Fatalf("remove resolution from fixture %s: %v", nodeID, err)
	}
}

func parentMigrationRefusalRecords(t *testing.T) map[string]string {
	t.Helper()
	records := make(map[string]string)
	prefixLength := len(fdbadapter.TestPrefixRange())

	for key, value := range storedKeyValues(t) {
		parts, err := tuple.Unpack([]byte(key)[prefixLength:])
		if err != nil || len(parts) == 0 {
			t.Fatalf("decode parent migration fixture key %x: %v", key, err)
		}

		switch parts[0] {
		case "node_instance", "node_view", "node_resolve", "node_by_property",
			"relationship", "relationship_reverse", "property_def", "property_def_by_name",
			"node_type_def", "node_type_by_key":
			records[key] = value
		}
	}

	return records
}
