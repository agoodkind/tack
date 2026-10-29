package integration_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
)

const gettingStartedURI = "tack://getting-started"

func epicScopedArguments(harness *integration.MCPHarness, epic, name string) datagen.ToolArguments {
	arguments := parentIntegrityArguments(harness, "")
	arguments.EpicReference = epic
	arguments.Name = name
	return arguments
}

// createProjectEpic creates an epic under harness.Project using name.
func createProjectEpic(t *testing.T, harness *integration.MCPHarness, name string) datagen.Result {
	t.Helper()
	arguments := parentIntegrityArguments(harness, harness.Project)
	arguments.Name = name
	return harness.Call(t, "tack_create_epic", arguments)
}

// listedUnder calls tool with an empty arguments.Name and returns the response text.
func listedUnder(t *testing.T, harness *integration.MCPHarness, tool string, arguments datagen.ToolArguments) string {
	t.Helper()
	arguments.Name = ""
	return harness.Call(t, tool, arguments).Text()
}

// TestCreateIssueFromGeneratedGuidanceUnderEpic checks issue creation from guidance.
// It checks the epic parent, project scope, and project identifier reference prefix.
// It checks that project and epic lists both include the issue.
func TestCreateIssueFromGeneratedGuidanceUnderEpic(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	stores := parentIntegrityStores(t)
	epicResult := createProjectEpic(t, harness, "Guided parent epic")
	epicID := epicResult.RawID()
	epicReference := printedReference(t, epicResult.Text())
	guidance := harness.ReadResource(t, gettingStartedURI)
	required, alternatives := guidedCreateInputs(t, guidance, "tack_create_issue")
	if !strings.Contains(strings.Join(alternatives, " "), "epic_reference") {
		t.Fatalf("guidance lists %v as alternatives, want epic_reference among them", alternatives)
	}
	values := map[string]string{
		"workspace_reference": harness.Workspace, "name": "Issue created from guidance",
		"project_reference": harness.Project, "epic_reference": epicReference,
	}

	result := harness.Call(t, "tack_create_issue", guidedArguments(t, values, append(required, "epic_reference")))

	issueID := result.RawID()
	view := storedParentView(t, stores, issueID)
	projectID := stringProperty(t, storedParentView(t, stores, epicID), "scope_id")
	requireParents(t, stores, issueID, epicID, []string{epicID})
	if scope := stringProperty(t, view, "scope_id"); scope != projectID {
		t.Fatalf("issue %s has scope_id %s, want project %s", issueID, scope, projectID)
	}
	if reference := printedReference(t, result.Text()); !strings.HasPrefix(reference, harness.Project+"-") {
		t.Fatalf("issue reference %q does not start with the project identifier %q", reference, harness.Project)
	}
	for _, listing := range []datagen.ToolArguments{
		parentIntegrityArguments(harness, harness.Project), epicScopedArguments(harness, epicReference, ""),
	} {
		if text := listedUnder(t, harness, "tack_list_issues", listing); !strings.Contains(text, "Issue created from guidance") {
			t.Fatalf("tack_list_issues %+v does not list the issue:\n%s", listing, text)
		}
	}
}

// TestCreateIssueWithProjectOnlyStaysOutOfEpicList checks that an issue created
// with only project_reference appears in the project list and does not appear
// in the epic list.
func TestCreateIssueWithProjectOnlyStaysOutOfEpicList(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	epicReference := printedReference(t, createProjectEpic(t, harness, "Empty epic").Text())
	arguments := parentIntegrityArguments(harness, harness.Project)
	arguments.Name = "Issue directly under the project"

	harness.Call(t, "tack_create_issue", arguments)

	projectList := listedUnder(t, harness, "tack_list_issues", parentIntegrityArguments(harness, harness.Project))
	if !strings.Contains(projectList, arguments.Name) {
		t.Fatalf("project list does not show the issue:\n%s", projectList)
	}
	if epicList := listedUnder(t, harness, "tack_list_issues", epicScopedArguments(harness, epicReference, "")); strings.Contains(epicList, arguments.Name) {
		t.Fatalf("epic list shows an issue created under the project:\n%s", epicList)
	}
}

// TestCreateEpicWithOnlyEpicReference checks that an epic created with epic_reference
// and without project_reference uses the parent epic and its project scope.
func TestCreateEpicWithOnlyEpicReference(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	stores := parentIntegrityStores(t)
	parent := createProjectEpic(t, harness, "Outer epic")

	child := harness.Call(t, "tack_create_epic", epicScopedArguments(harness, printedReference(t, parent.Text()), "Inner epic"))

	requireParents(t, stores, child.RawID(), parent.RawID(), []string{parent.RawID()})
	parentScope := stringProperty(t, storedParentView(t, stores, parent.RawID()), "scope_id")
	if scope := stringProperty(t, storedParentView(t, stores, child.RawID()), "scope_id"); scope != parentScope {
		t.Fatalf("inner epic has scope_id %s, want %s", scope, parentScope)
	}
}

// TestCreateIssueRefusesMissingOrConflictingScope checks that issue creation rejects
// missing parent references, both leaf references, and epic_reference combined
// with properties.parent_id.
func TestCreateIssueRefusesMissingOrConflictingScope(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	epicReference := printedReference(t, createProjectEpic(t, harness, "Conflict epic").Text())
	both := epicScopedArguments(harness, epicReference, "Issue with two parents")
	both.ProjectReference = harness.Project
	withParentProperty := epicScopedArguments(harness, epicReference, "Issue with two parent choices")
	withParentProperty.Properties = datagen.NodeProperties{"parent_id": json.RawMessage(strconv.Quote(epicReference))}
	cases := map[string]struct {
		arguments datagen.ToolArguments
		want      string
	}{
		"no scope":            {epicScopedArguments(harness, "", "Issue without scope"), "exactly one of"},
		"two scopes":          {both, "exactly one of"},
		"parent and property": {withParentProperty, "exactly one of epic_reference or properties.parent_id"},
	}
	for name, test := range cases {
		if text := harness.CallExpectError(t, "tack_create_issue", test.arguments); !strings.Contains(text, test.want) {
			t.Fatalf("%s: error %q does not contain %q", name, text, test.want)
		}
	}
}

// TestCreateCommentWithProjectAndIssueReference checks that a comment created with
// project_reference and issue_reference appears in the issue's comment list.
func TestCreateCommentWithProjectAndIssueReference(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	issue := harness.CreateIssue(t, "Commented issue")
	arguments := parentIntegrityArguments(harness, harness.Project)
	arguments.IssueReference = issue
	arguments.Name = "Comment on the issue"

	harness.Call(t, "tack_create_comment", arguments)

	if text := listedUnder(t, harness, "tack_list_comments", arguments); !strings.Contains(text, "Comment on the issue") {
		t.Fatalf("comment list does not show the new comment:\n%s", text)
	}
}
