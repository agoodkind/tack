package integration_test

import (
	"testing"

	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
)

const gettingStartedURI = "tack://getting-started"

const exactlyOneIssueScope = "exactly one of project_reference, epic_reference, cycle_reference, module_reference, issue_reference is required"

func epicScopedArguments(harness *integration.MCPHarness, epic, name string) datagen.ToolArguments {
	arguments := parentIntegrityArguments(harness, "")
	arguments.EpicReference = epic
	arguments.Name = name

	return arguments
}

func createProjectEpic(t *testing.T, harness *integration.MCPHarness, name string) datagen.Result {
	t.Helper()
	arguments := parentIntegrityArguments(harness, harness.Project)
	arguments.Name = name

	return harness.Call(t, "tack_create_epic", arguments)
}

func listedUnder(
	t *testing.T,
	harness *integration.MCPHarness,
	tool string,
	arguments datagen.ToolArguments,
) string {
	t.Helper()
	arguments.Name = ""

	return harness.Call(t, tool, arguments).Text()
}
