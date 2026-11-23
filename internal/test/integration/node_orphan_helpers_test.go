package integration

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/testenv"
)

// storedNodes reads every node record under the active test prefix straight
// from the node_instance key family.
func storedNodes(t *testing.T) []node.Node {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), testTransactionTimeout)
	if err != nil {
		t.Fatalf("open database to read nodes: %v", err)
	}

	prefix := append([]byte{}, fdbadapter.TestPrefixRange()...)
	keyRange, err := fdb.PrefixRange(append(prefix, tuple.Tuple{"node_instance"}.Pack()...))
	if err != nil {
		t.Fatalf("create node_instance range: %v", err)
	}

	items, err := database.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		t.Fatalf("read node_instance range: %v", err)
	}

	stored := items.([]fdb.KeyValue)

	nodes := make([]node.Node, 0, len(stored))
	for _, item := range stored {
		var current node.Node
		if err := json.Unmarshal(item.Value, &current); err != nil {
			t.Fatalf("decode node record: %v", err)
		}

		nodes = append(nodes, current)
	}

	return nodes
}

// directChildren returns the nodes with a child_of edge to parentID.
func directChildren(t *testing.T, env *TestEnv, parentID uuid.UUID) []uuid.UUID {
	t.Helper()
	edges, err := env.Stores.Relationships.ListByTarget(
		env.Ctx,
		env.OrgID,
		parentID,
		node.RelChildOf,
	)
	if err != nil {
		t.Fatalf("read children of node %s: %v", parentID, err)
	}

	children := []uuid.UUID{}

	for _, edge := range edges {
		children = append(children, edge.SourceID)
	}

	if len(children) == 0 {
		t.Fatalf("node %s has no direct children", parentID)
	}

	return children
}

// resolveNode returns the resolution record of nodeID, or nil when the node
// does not exist.
func resolveNode(t *testing.T, env *TestEnv, nodeID uuid.UUID) *node.NodeResolve {
	t.Helper()
	resolved, err := env.Stores.Views.Resolve(env.Ctx, nodeID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}

	if err != nil {
		t.Fatalf("resolve node %s: %v", nodeID, err)
	}

	return resolved
}

// requireNoDanglingParents fails when a child_of target does not have a
// resolution record.
func requireNoDanglingParents(t *testing.T, env *TestEnv) {
	t.Helper()

	for _, current := range storedNodes(t) {
		edges, err := env.Stores.Relationships.ListBySource(
			env.Ctx,
			current.OrgID,
			current.ID,
			node.RelChildOf,
		)
		if err != nil {
			t.Fatalf("read parent edges of node %s: %v", current.ID, err)
		}

		for _, edge := range edges {
			if resolveNode(t, env, edge.TargetID) == nil {
				t.Errorf(
					"%s %s has a child_of edge to absent node %s",
					current.NodeType,
					current.ID,
					edge.TargetID,
				)
			}
		}
	}
}

// requireNodesGone fails when any node in nodeIDs still resolves.
func requireNodesGone(t *testing.T, env *TestEnv, nodeIDs []uuid.UUID) {
	t.Helper()

	for _, nodeID := range nodeIDs {
		if resolved := resolveNode(t, env, nodeID); resolved != nil {
			t.Errorf("%s %s still exists after the delete", resolved.NodeType, nodeID)
		}
	}
}

// requireNodesPresent fails when any node in nodeIDs no longer resolves.
func requireNodesPresent(t *testing.T, env *TestEnv, nodeIDs []uuid.UUID) {
	t.Helper()

	for _, nodeID := range nodeIDs {
		if resolveNode(t, env, nodeID) == nil {
			t.Errorf("node %s was deleted, and it is not a descendant of the deleted node", nodeID)
		}
	}
}
