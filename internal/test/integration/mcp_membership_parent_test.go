package integration

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/testenv"
)

// TestMembershipEdgesAreNotParents checks that adding member_of relationships
// preserves the issue's project parent and search access. Calling
// tack_add_relationship with child_of replaces the project parent.
func TestMembershipEdgesAreNotParents(t *testing.T) {
	harness := NewMCPHarness(t)
	issue := harness.CreateIssue(t, "member issue")
	issueID := harness.rawID(t, "tack_get_issue", issue)
	containers := make([]string, 0, 3)

	for _, tool := range []string{"tack_create_cycle", "tack_create_module", "tack_create_epic"} {
		arguments := harness.projectArgs()
		arguments.Name = "Container " + tool
		rawID := harness.Call(t, tool, arguments).RawID()
		if rawID == "" {
			t.Fatalf("%s returned no raw id", tool)
		}

		containers = append(containers, rawID)
		harness.Call(
			t,
			"tack_add_relationship",
			relationshipToolArguments(issue, "member_of", rawID),
		)
	}

	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), testTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}

	if _, err := stores.SearchPolicySet().Index(
		t.Context(),
		searchaccess.IndexAccessRequest{
			Version:        searchaccess.StableVersion,
			OrganizationID: harness.orgID,
			ResourceID:     uuid.MustParse(issueID),
			Generation:     1,
		},
	); err != nil {
		t.Errorf("search access of issue %s with member_of edges: %v", issueID, err)
	}

	parents := harness.outgoingRelationships(t, issue, "child_of")
	if !strings.Contains(parents, "- Target: `"+harness.Project+"`") {
		t.Fatalf("membership changed the parent of issue %s:\n%s", issue, parents)
	}

	harness.Call(
		t,
		"tack_add_relationship",
		relationshipToolArguments(issue, "child_of", containers[0]),
	)
	parents = harness.outgoingRelationships(t, issue, "child_of")
	if strings.Contains(parents, "- Target: `"+harness.Project+"`") {
		t.Fatalf("issue %s kept its previous parent after the move:\n%s", issue, parents)
	}

	edges, err := stores.Relationships.ListBySource(
		t.Context(),
		harness.orgID,
		uuid.MustParse(issueID),
		"child_of",
	)
	if err != nil || len(edges) != 1 || edges[0].TargetID.String() != containers[0] {
		t.Fatalf("parent edges after the move = %v, %v, want %s", edges, err, containers[0])
	}
}
