package ops

import (
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

func TestSelectRepairNodeFallsBackToViewAndWarnsOnMissingRows(t *testing.T) {
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
				Props:     map[string][]byte{"slug": mustRaw(t, "issue-1")},
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

func TestSelectRepairNodeWarnsWhenPayloadUnavailable(t *testing.T) {
	nodeID := uuid.New()
	report := &fdbadapter.NodeInspectionReport{NodeID: nodeID}

	selected := selectRepairNode(report)

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

func TestBuildIndexedPropertyChecksCountsOnlyExactMatches(t *testing.T) {
	orgID := uuid.New()
	currentNode := &node.Node{
		OrgID:    orgID,
		NodeType: "issue",
		Props: map[string][]byte{
			"state_id": mustRaw(t, "state-a"),
			"slug":     mustRaw(t, "issue-1"),
		},
	}
	defs := []*node.PropertyDef{{Name: "state_id", Indexed: true}, {Name: "slug", Indexed: false}}
	report := &fdbadapter.NodeInspectionReport{PropertyIndexRows: []fdbadapter.InspectPropertyIndexRow{
		{OrgID: orgID, NodeType: "issue", PropertyName: "state_id", EncodedValue: mustRaw(t, "state-a")},
		{OrgID: orgID, NodeType: "issue", PropertyName: "state_id", EncodedValue: mustRaw(t, "state-a")},
		{OrgID: orgID, NodeType: "issue", PropertyName: "state_id", EncodedValue: mustRaw(t, "state-b")},
		{OrgID: orgID, NodeType: "project", PropertyName: "state_id", EncodedValue: mustRaw(t, "state-a")},
	}}

	checks := buildIndexedPropertyChecks(currentNode, defs, report)

	if len(checks) != 1 {
		t.Fatalf("checks len = %d want 1", len(checks))
	}
	if checks[0].PropertyName != "state_id" {
		t.Fatalf("PropertyName = %q want state_id", checks[0].PropertyName)
	}
	if checks[0].MatchingRowCount != 2 {
		t.Fatalf("MatchingRowCount = %d want 2", checks[0].MatchingRowCount)
	}
}

func TestBuildSlugChecksReturnsNilWithoutSlugAndCountsMatches(t *testing.T) {
	if checks := buildSlugChecks(&node.Node{NodeType: "issue"}, &fdbadapter.NodeInspectionReport{}); checks != nil {
		t.Fatalf("checks = %v want nil", checks)
	}

	report := &fdbadapter.NodeInspectionReport{SlugRows: []fdbadapter.InspectSlugRow{
		{NodeType: "issue", Slug: "issue-1"},
		{NodeType: "issue", Slug: "issue-1"},
		{NodeType: "issue", Slug: "issue-2"},
		{NodeType: "project", Slug: "issue-1"},
	}}
	checks := buildSlugChecks(
		&node.Node{NodeType: "issue", Props: map[string][]byte{"slug": mustRaw(t, "issue-1")}},
		report,
	)

	if len(checks) != 1 {
		t.Fatalf("checks len = %d want 1", len(checks))
	}
	if checks[0].MatchingRowCount != 2 {
		t.Fatalf("MatchingRowCount = %d want 2", checks[0].MatchingRowCount)
	}
}
