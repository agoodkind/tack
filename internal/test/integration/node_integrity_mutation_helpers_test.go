package integration_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// mutationSnapshot reads the mutation snapshot of nodeID through the node
// store.
func mutationSnapshot(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID) (node.MutationSnapshot, node.SchemaSnapshot) {
	t.Helper()
	snapshot, schema, err := env.Stores.Nodes.ReadMutationSnapshot(env.Ctx, env.OrgID, nodeID)
	if err != nil {
		t.Fatalf("read the mutation snapshot of %s: %v", nodeID, err)
	}
	return snapshot, schema
}

// renamePlan returns a plan that renames the node in snapshot and requires
// records to match.
func renamePlan(snapshot node.MutationSnapshot, schema node.SchemaSnapshot, name string, records []node.GraphRecord) node.MutationPlan {
	return node.MutationPlan{
		OrgID: snapshot.Node.OrgID, NodeID: snapshot.Node.ID, NodeType: snapshot.Node.NodeType, ActorID: uuid.New(),
		Source:        node.SourceExpectation{Absent: false, PrimaryDigest: snapshot.PrimaryDigest},
		MetadataEpoch: schema.MetadataEpoch,
		Graph:         node.GraphExpectation{Records: records, Relationships: nil, Incoming: nil},
		Name:          &name, Props: map[string]json.RawMessage{}, Parent: nil, ReferenceKeys: nil,
		Relationships: node.RelationshipChanges{Add: nil, Remove: nil}, Idempotency: nil,
	}
}

// movePlan returns a plan that moves the node in snapshot under parentID and
// requires the parent chain of parentID and the other child_of edges of the
// node to match.
func movePlan(t *testing.T, env *integration.TestEnv, snapshot node.MutationSnapshot, schema node.SchemaSnapshot, parentID uuid.UUID) node.MutationPlan {
	t.Helper()
	plan := renamePlan(snapshot, schema, snapshot.Node.Name, parentChain(t, env, parentID))
	plan.Name = nil
	plan.Parent = &node.ParentChange{TargetID: parentID}
	for _, edge := range snapshot.Outgoing {
		if edge.Relationship.RelationType == node.RelChildOf && edge.Relationship.TargetID != parentID {
			plan.Graph.Relationships = append(plan.Graph.Relationships, node.RelationshipExpectation{
				RelationType: node.RelChildOf, TargetID: edge.Relationship.TargetID, Digest: edge.Digest,
			})
		}
	}
	return plan
}

// parentChain returns the graph record of parentID and each ancestor along
// the stored parent_id chain.
func parentChain(t *testing.T, env *integration.TestEnv, parentID uuid.UUID) []node.GraphRecord {
	t.Helper()
	records := []node.GraphRecord{}
	for currentID := parentID; currentID != uuid.Nil; {
		snapshot, _ := mutationSnapshot(t, env, currentID)
		if !snapshot.Exists {
			break
		}
		records = append(records, node.GraphRecord{NodeID: currentID, PrimaryDigest: snapshot.PrimaryDigest})
		currentID = snapshot.ParentID
	}
	return records
}

// deletionPlan returns a plan that deletes the node in snapshot and requires
// every relationship in snapshot to match.
func deletionPlan(snapshot node.MutationSnapshot) node.DeletionPlan {
	outgoing := []node.RelationshipExpectation{}
	for _, edge := range snapshot.Outgoing {
		outgoing = append(outgoing, node.RelationshipExpectation{
			RelationType: edge.Relationship.RelationType, TargetID: edge.Relationship.TargetID, Digest: edge.Digest,
		})
	}
	incoming := []node.IncomingRelationshipExpectation{}
	for _, edge := range snapshot.Incoming {
		incoming = append(incoming, node.IncomingRelationshipExpectation{
			SourceID: edge.Relationship.SourceID, RelationType: edge.Relationship.RelationType, Digest: edge.Digest,
		})
	}
	return node.DeletionPlan{
		OrgID: snapshot.Node.OrgID, NodeID: snapshot.Node.ID, ActorID: uuid.New(),
		Source: node.SourceExpectation{Absent: false, PrimaryDigest: snapshot.PrimaryDigest},
		Graph:  node.GraphExpectation{Records: nil, Relationships: outgoing, Incoming: incoming},
	}
}

// moveUpdate returns the service update that sets parent_id of nodeID to
// parentID.
func moveUpdate(nodeID, parentID uuid.UUID) service.UpdateInput {
	return service.UpdateInput{
		NodeID: nodeID, Name: nil, Props: schemaProps("parent_id", quoted(parentID.String())),
		RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
	}
}

// requireUnchanged fails if the stored primary record of the node in before
// has changed.
func requireUnchanged(t *testing.T, env *integration.TestEnv, before node.MutationSnapshot) {
	t.Helper()
	after, _ := mutationSnapshot(t, env, before.Node.ID)
	if !after.Exists || after.PrimaryDigest != before.PrimaryDigest {
		t.Fatalf("node %s changed after a refused mutation: exists %t", before.Node.ID, after.Exists)
	}
}

// requireError fails unless err wraps want.
func requireError(t *testing.T, err, want error, operation string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s returned %v, want an error that wraps %v", operation, err, want)
	}
}
