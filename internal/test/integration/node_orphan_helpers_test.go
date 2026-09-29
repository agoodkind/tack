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

// directChildren returns the nodes with parent_id equal to parentID.
func directChildren(t *testing.T, parentID uuid.UUID) []uuid.UUID {
	t.Helper()
	children := []uuid.UUID{}
	for _, current := range storedNodes(t) {
		var parentText string
		if err := json.Unmarshal(current.Props["parent_id"], &parentText); err == nil && parentText == parentID.String() {
			children = append(children, current.ID)
		}
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

// requireNoDanglingParents fails when the parent_id property of a stored node
// is the ID of a node without a resolution record.
func requireNoDanglingParents(t *testing.T, env *TestEnv) {
	t.Helper()
	for _, current := range storedNodes(t) {
		raw, found := current.Props["parent_id"]
		if !found {
			continue
		}
		var parentText string
		if err := json.Unmarshal(raw, &parentText); err != nil || parentText == "" {
			continue
		}
		parentID, err := uuid.Parse(parentText)
		if err != nil {
			t.Fatalf("node %s has parent_id %q that is not a UUID", current.ID, parentText)
		}
		if resolveNode(t, env, parentID) == nil {
			t.Errorf("%s %s has parent_id %s, and that node does not exist", current.NodeType, current.ID, parentID)
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
