package foundationdb

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func TestCollectOrgIDsDeduplicatesAndSorts(t *testing.T) {
	firstOrgID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	secondOrgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	report := &NodeInspectionReport{
		Resolve:           &node.NodeResolve{OrgID: firstOrgID},
		NodeInstances:     []InspectNodeInstance{{OrgID: secondOrgID}, {OrgID: firstOrgID}},
		NodeViews:         []InspectNodeView{{OrgID: secondOrgID}},
		PropertyIndexRows: []InspectPropertyIndexRow{{OrgID: firstOrgID}},
	}

	orgIDs := collectOrgIDs(report)

	if len(orgIDs) != 2 {
		t.Fatalf("orgIDs len = %d want 2", len(orgIDs))
	}
	if orgIDs[0] != secondOrgID || orgIDs[1] != firstOrgID {
		t.Fatalf("orgIDs = %v want sorted unique IDs", orgIDs)
	}
}

func TestSortInspectionReportOrdersEveryFamilyDeterministically(t *testing.T) {
	report := &NodeInspectionReport{
		NodeInstances: []InspectNodeInstance{
			{OrgID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), NodeType: "zeta"},
			{OrgID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), NodeType: "alpha"},
		},
		NodeViews: []InspectNodeView{
			{OrgID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), NodeType: "zeta"},
			{OrgID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), NodeType: "alpha"},
		},
		PropertyIndexRows: []InspectPropertyIndexRow{
			{OrgID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), NodeType: "issue", PropertyName: "slug", EncodedValue: []byte(`"z"`)},
			{OrgID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), NodeType: "issue", PropertyName: "slug", EncodedValue: []byte(`"a"`)},
		},
		SlugRows: []InspectSlugRow{{NodeType: "zeta", Slug: "z"}, {NodeType: "alpha", Slug: "a"}},
		Relationships: []InspectRelationshipRow{
			{OrgID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), RelationType: "relates_to", TargetID: uuid.MustParse("00000000-0000-0000-0000-000000000002")},
			{OrgID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), RelationType: "blocks", TargetID: uuid.MustParse("00000000-0000-0000-0000-000000000001")},
		},
		RelationshipReverseRows: []InspectRelationshipReverseRow{
			{OrgID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), RelationType: "relates_to", SourceID: uuid.MustParse("00000000-0000-0000-0000-000000000002")},
			{OrgID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), RelationType: "blocks", SourceID: uuid.MustParse("00000000-0000-0000-0000-000000000001")},
		},
	}

	sortInspectionReport(report)

	if report.NodeInstances[0].NodeType != "alpha" {
		t.Fatalf("NodeInstances[0].NodeType = %q want alpha", report.NodeInstances[0].NodeType)
	}
	if report.NodeViews[0].NodeType != "alpha" {
		t.Fatalf("NodeViews[0].NodeType = %q want alpha", report.NodeViews[0].NodeType)
	}
	if string(report.PropertyIndexRows[0].EncodedValue) != `"a"` {
		t.Fatalf("PropertyIndexRows[0].EncodedValue = %s want \"a\"", report.PropertyIndexRows[0].EncodedValue)
	}
	if report.SlugRows[0].NodeType != "alpha" {
		t.Fatalf("SlugRows[0].NodeType = %q want alpha", report.SlugRows[0].NodeType)
	}
	if report.Relationships[0].RelationType != "blocks" {
		t.Fatalf("Relationships[0].RelationType = %q want blocks", report.Relationships[0].RelationType)
	}
	if report.RelationshipReverseRows[0].RelationType != "blocks" {
		t.Fatalf("RelationshipReverseRows[0].RelationType = %q want blocks", report.RelationshipReverseRows[0].RelationType)
	}
}
