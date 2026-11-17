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

func removeMigrationPrimaryFixture(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID) {
	t.Helper()
	current, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, nodeID)
	if err != nil || current == nil {
		t.Fatalf("read migration primary fixture %s: %v", nodeID, err)
	}

	changeMigrationPreflightRecord(
		t,
		func(transaction fdb.Transaction) {
			transaction.Clear(
				parentMigrationKey(tuple.Tuple{
					"node_instance",
					env.OrgID.String(),
					current.NodeType,
					nodeID.String(),
				}),
			)
		},
	)
}

func storeMigrationScopeFixture(
	t *testing.T,
	env *integration.TestEnv,
	nodeID uuid.UUID,
	value json.RawMessage,
) {
	t.Helper()
	current, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, nodeID)
	if err != nil || current == nil {
		t.Fatalf("read migration scope fixture %s: %v", nodeID, err)
	}

	current.Props["scope_id"] = value
	encoded, err := json.Marshal(current)
	if err != nil {
		t.Fatalf("encode migration scope fixture %s: %v", nodeID, err)
	}

	changeMigrationPreflightRecord(
		t,
		func(transaction fdb.Transaction) {
			transaction.Set(
				parentMigrationKey(tuple.Tuple{
					"node_instance",
					env.OrgID.String(),
					current.NodeType,
					nodeID.String(),
				}),
				encoded,
			)
		},
	)
}

func changeMigrationPreflightRecord(t *testing.T, apply func(fdb.Transaction)) {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), nodeAuditTransactionTimeout)
	if err != nil {
		t.Fatalf("open migration preflight fixture store: %v", err)
	}

	transaction, err := database.CreateTransaction()
	if err != nil {
		t.Fatalf("create migration preflight fixture transaction: %v", err)
	}
	defer transaction.Cancel()
	apply(transaction)
	if err := transaction.Commit().Get(); err != nil {
		t.Fatalf("commit migration preflight fixture: %v", err)
	}
}
