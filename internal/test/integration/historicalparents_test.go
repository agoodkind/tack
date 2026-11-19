package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

func storeLegacyParentFixture(
	t *testing.T,
	env *integration.TestEnv,
	nodeID uuid.UUID,
	legacyParentID uuid.UUID,
	removeEdge bool,
) {
	t.Helper()
	current, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, nodeID)
	if err != nil || current == nil {
		t.Fatalf("read legacy fixture node %s: %v", nodeID, err)
	}

	view, err := env.Stores.Views.Get(env.Ctx, nodeID)
	if err != nil || view == nil {
		t.Fatalf("read legacy fixture view %s: %v", nodeID, err)
	}

	edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, nodeID, node.RelChildOf)
	if err != nil {
		t.Fatalf("read legacy fixture relationships of %s: %v", nodeID, err)
	}

	value, err := json.Marshal(legacyParentID.String())
	if err != nil {
		t.Fatalf("encode legacy parent %s: %v", legacyParentID, err)
	}

	current.Props["parent_id"], view.Props["parent_id"] = value, value
	primary, err := json.Marshal(current)
	if err != nil {
		t.Fatalf("encode legacy fixture node %s: %v", nodeID, err)
	}

	readView, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("encode legacy fixture view %s: %v", nodeID, err)
	}

	database, err := fdbadapter.Open(testenv.FoundationDB(t), nodeAuditTransactionTimeout)
	if err != nil {
		t.Fatalf("open legacy fixture store: %v", err)
	}

	_, err = database.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(
			parentMigrationKey(tuple.Tuple{
				"node_instance",
				env.OrgID.String(),
				current.NodeType,
				nodeID.String(),
			}),
			primary,
		)
		tr.Set(
			parentMigrationKey(tuple.Tuple{
				"node_view",
				env.OrgID.String(),
				current.NodeType,
				nodeID.String(),
			}),
			readView,
		)
		tr.Set(
			parentMigrationKey(tuple.Tuple{
				"node_by_property",
				env.OrgID.String(),
				current.NodeType,
				"parent_id",
				value,
				nodeID.String(),
			}),
			[]byte{},
		)
		if removeEdge {
			for _, edge := range edges {
				tr.Clear(
					parentMigrationKey(tuple.Tuple{
						"relationship",
						env.OrgID.String(),
						nodeID.String(),
						node.RelChildOf,
						edge.TargetID.String(),
					}),
				)
				tr.Clear(
					parentMigrationKey(tuple.Tuple{
						"relationship_reverse",
						env.OrgID.String(),
						edge.TargetID.String(),
						node.RelChildOf,
						nodeID.String(),
					}),
				)
			}
		}

		return nil, nil
	})
	if err != nil {
		t.Fatalf("store legacy parent fixture for node %s: %v", nodeID, err)
	}
}

func parentMigrationKey(values tuple.Tuple) fdb.Key {
	return fdb.Key(append(append([]byte{}, fdbadapter.TestPrefixRange()...), values.Pack()...))
}

func requireMigratedParent(t *testing.T, env *integration.TestEnv, nodeID, parentID uuid.UUID) {
	t.Helper()
	current, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, nodeID)
	if err != nil || current == nil {
		t.Fatalf("read migrated node %s: %v", nodeID, err)
	}

	view, err := env.Stores.Views.Get(env.Ctx, nodeID)
	if err != nil || view == nil {
		t.Fatalf("read migrated view %s: %v", nodeID, err)
	}

	for _, props := range []map[string]json.RawMessage{current.Props, view.Props} {
		if _, present := props["parent_id"]; present {
			t.Fatalf("node %s still stores parent_id", nodeID)
		}
	}

	parents, err := env.Stores.Views.List(
		env.Ctx,
		node.NodeListQuery{
			OrgID:            env.OrgID,
			BySourceRelation: &node.RelationScan{NodeID: nodeID, RelationType: node.RelChildOf},
		},
	)
	if err != nil || len(parents) != 1 || parents[0].ID != parentID {
		t.Fatalf("parents of %s = %+v, error %v, want only %s", nodeID, parents, err, parentID)
	}

	legacy, err := env.Stores.Nodes.ListByProperty(
		env.Ctx,
		env.OrgID,
		current.NodeType,
		"parent_id",
		quoted(parentID.String()),
	)
	if err != nil || len(legacy) != 0 {
		t.Fatalf("legacy index for %s = %+v, error %v, want an empty index", parentID, legacy, err)
	}
}
