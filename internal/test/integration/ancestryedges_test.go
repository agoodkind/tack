package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// childOfEdge lists the stored child_of edges of sourceID through the
// relationship store and returns the edge that targets targetID. It fails
// the test when the node does not have that edge.
func childOfEdge(
	t *testing.T,
	env *integration.TestEnv,
	sourceID uuid.UUID,
	targetID uuid.UUID,
) node.Relationship {
	t.Helper()
	edges, err := env.Stores.Relationships.ListBySource(
		env.Ctx,
		env.OrgID,
		sourceID,
		node.RelChildOf,
	)
	if err != nil {
		t.Fatalf("list child_of edges of %s: %v", sourceID, err)
	}

	for _, edge := range edges {
		if edge.TargetID == targetID {
			return *edge
		}
	}

	t.Fatalf("node %s has no child_of edge to %s", sourceID, targetID)

	return node.Relationship{}
}

// parentEntry returns an executable entry that sets parentID as the parent
// and removes the listed edges. A nil parentID keeps the current parent.
func parentEntry(
	repairID string,
	nodeID uuid.UUID,
	parentID uuid.UUID,
	remove ...ops.RepairEdge,
) ops.NodeIntegrityEntry {
	result := ops.RepairResult{Props: map[string]json.RawMessage{}, RemoveEdges: remove}
	if parentID != uuid.Nil {
		result.Props["parent_id"] = textValue(parentID.String())
	}

	return ops.NodeIntegrityEntry{
		RepairID: repairID,
		NodeID:   nodeID,
		NodeType: "issue",
		Status:   ops.RepairEntryExecutable,
		Classes:  []string{"parent_mapping"},
		Evidence: []ops.RepairEvidence{
			{
				Kind:      ops.RepairEvidenceInferred,
				Reference: "TACK-582 parent mappings",
				Summary:   "The selected parent is the most specific linked container.",
			},
		},
		Result: result,
	}
}

func addLegacyParentEdge(t *testing.T, env *integration.TestEnv, sourceID, targetID uuid.UUID) {
	t.Helper()
	if err := env.Stores.Relationships.Add(
		env.Ctx,
		&node.Relationship{
			OrgID:        env.OrgID,
			SourceID:     sourceID,
			RelationType: node.RelChildOf,
			TargetID:     targetID,
			CreatedBy:    uuid.New(),
			CreatedAt:    time.Now().UTC(),
			Props:        nil,
		},
	); err != nil {
		t.Fatalf("add the legacy child_of edge from %s to %s: %v", sourceID, targetID, err)
	}
}
