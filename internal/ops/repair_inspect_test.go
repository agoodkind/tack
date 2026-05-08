package ops

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

func TestSelectNodeFallsBackToViewAndWarnsOnMissingRows(t *testing.T) {
	nodeID := uuid.New()
	orgID := uuid.New()
	updatedAt := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	report := &fdbadapter.NodeInspectionReport{
		NodeID: nodeID,
		NodeViews: []fdbadapter.InspectNodeView{{
			OrgID:    orgID,
			NodeType: "issue",
			NodeID:   nodeID,
			Value: &node.NodeView{
				ID:        nodeID,
				OrgID:     orgID,
				NodeType:  "issue",
				Name:      "Issue",
				UpdatedAt: updatedAt,
				Props:     map[string]json.RawMessage{"identifier": mustRaw(t, "issue-1")},
			},
		}},
	}

	selected := selectRepairNode(report)
	if selected.Node == nil {
		t.Fatal("selected Node = nil")
	}
	if selected.Node.ID != nodeID {
		t.Fatalf("selected Node ID = %s want %s", selected.Node.ID, nodeID)
	}
	if selected.Node.UpdatedAt != updatedAt {
		t.Fatalf("selected Node UpdatedAt = %s want %s", selected.Node.UpdatedAt, updatedAt)
	}
	if len(selected.Warnings) != 2 {
		t.Fatalf("Warnings len = %d want 2", len(selected.Warnings))
	}
	if selected.Warnings[0] != "node_resolve row missing" {
		t.Fatalf("Warnings[0] = %q", selected.Warnings[0])
	}
	if selected.Warnings[1] != "node_instance row missing" {
		t.Fatalf("Warnings[1] = %q", selected.Warnings[1])
	}
}

func TestSelectNodeWarnsWhenPayloadUnavailable(t *testing.T) {
	nodeID := uuid.New()
	selected := selectRepairNode(&fdbadapter.NodeInspectionReport{NodeID: nodeID})
	if selected.Node != nil {
		t.Fatalf("selected Node = %+v want nil", selected.Node)
	}
	if len(selected.Warnings) != 4 {
		t.Fatalf("Warnings len = %d want 4", len(selected.Warnings))
	}
	if selected.Warnings[3] != "selected node payload unavailable" {
		t.Fatalf("Warnings[3] = %q", selected.Warnings[3])
	}
}
