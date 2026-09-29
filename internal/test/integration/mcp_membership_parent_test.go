package integration

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/testenv"
)

// reservedParentText is part of the error tack_add_relationship returns for
// the child_of relation type.
const reservedParentText = "is reserved for the parent that parent_id sets"

// TestMembershipEdgesAreNotParents creates an issue under the harness project
// and adds member_of edges from it to a cycle, a module, and an epic through
// tack_add_relationship. Only child_of edges define a hierarchy parent. The
// issue must keep the project as its one hierarchy parent, and search access
// must compile for it. tack_add_relationship must refuse a child_of edge with
// the reserved relation type error and add no edge.
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
		harness.Call(t, "tack_add_relationship", relationshipToolArguments(issue, "member_of", rawID))
	}

	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), testTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	if _, err := stores.SearchPolicySet().Index(t.Context(), searchaccess.IndexAccessRequest{
		Version: searchaccess.StableVersion, OrganizationID: harness.orgID, ResourceID: uuid.MustParse(issueID), Generation: 1,
	}); err != nil {
		t.Errorf("search access of issue %s with member_of edges: %v", issueID, err)
	}

	refusal := harness.CallExpectError(t, "tack_add_relationship", relationshipToolArguments(issue, "child_of", containers[0]))
	if !strings.Contains(refusal, reservedParentText) {
		t.Fatalf("child_of add failed with %q, want the reserved relation type error", refusal)
	}
	parents := harness.outgoingRelationships(t, issue, "child_of")
	if strings.Contains(parents, containers[0]) || !strings.Contains(parents, "- Target: `"+harness.Project+"`") {
		t.Fatalf("child_of edges of issue %s after the refused add:\n%s", issue, parents)
	}
}
