package integration

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
)

// TestDatagenCascadeProbeWaitsForRunningDeletion exercises interrupted-run
// cleanup with enough real descendants to exceed the public delete budget.
func TestDatagenCascadeProbeWaitsForRunningDeletion(t *testing.T) {
	harness := NewAuditedMCPHarness(t)
	seed := nextHarnessSeed()
	identifier := fmt.Sprintf("DEL%06X", uint64(seed)&0xFFFFFF)
	name := "QA cascade delete " + identifier
	project := harness.Call(t, "tack_create_project", datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, Name: name,
		Properties: datagen.NodeProperties{"identifier": json.RawMessage(strconv.Quote(identifier))},
	})
	var firstIssue string
	for number := range 512 {
		issue := harness.Call(t, "tack_create_issue", datagen.ToolArguments{
			WorkspaceReference: harness.Workspace, ProjectReference: project.RawID(),
			Name: fmt.Sprintf("Background deletion issue %d", number),
		})
		if number == 0 {
			firstIssue = issue.RawID()
		}
	}
	comment := harness.Call(t, "tack_create_comment", datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, ProjectReference: project.RawID(), IssueReference: firstIssue,
		Name: "Background deletion comment",
	})
	scale, err := datagen.ParseScale("small")
	if err != nil {
		t.Fatal(err)
	}
	identities := datagen.Identities{Workspaces: []datagen.WorkspaceIdentity{{
		OrgID: harness.orgID, Slug: harness.Workspace,
		Actors: []datagen.Actor{{Token: harness.token}},
	}}}
	generator := datagen.NewGenerator(harness.driver, nil, identities, scale, seed, datagen.GeneratorOptions{})
	result, err := generator.VerifyCascadeDelete(t.Context())
	if err != nil {
		t.Fatalf("verify asynchronous cascade: %v; evidence=%+v", err, result)
	}
	if len(result.Deletes) != 2 || result.Deletes[0].InitialState != "running" || result.Deletes[0].JobID == "" {
		t.Fatalf("the real residual delete did not return a running job: %+v", result)
	}
	for _, check := range []struct{ tool, nodeID string }{
		{tool: "tack_get_project", nodeID: project.RawID()},
		{tool: "tack_get_issue", nodeID: firstIssue},
		{tool: "tack_get_comment", nodeID: comment.RawID()},
	} {
		text := harness.CallExpectError(t, check.tool, datagen.ToolArguments{WorkspaceReference: harness.Workspace, NodeID: check.nodeID})
		if !strings.Contains(text, "not found") {
			t.Fatalf("completed cascade read %s returned %q", check.tool, text)
		}
	}
	t.Logf("cascade completed after initial running job=%s, descendants=%d", result.Deletes[0].JobID, 513)
}
