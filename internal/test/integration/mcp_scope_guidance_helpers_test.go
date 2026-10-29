package integration_test

import (
	"regexp"
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
)

var guidanceParamPattern = regexp.MustCompile("`([a-z][a-z_]*)`( \\(for example `[^`]*`\\))?")

var createdReferencePattern = regexp.MustCompile("- Reference: `([^`]+)`")

const (
	guidanceRequiredPrefix     = "  - Required: "
	guidanceAlternativesPrefix = "  - Exactly one of: "
)

// guidedCreateInputs reads required and alternative parameters for tool from guidance.
// It fails the test if guidance does not contain an entry for tool.
func guidedCreateInputs(t *testing.T, guidance, tool string) ([]string, []string) {
	t.Helper()
	lines := strings.Split(guidance, "\n")
	for index, line := range lines {
		if line != "- `"+tool+"`" {
			continue
		}
		var required, alternatives []string
		for _, next := range lines[index+1:] {
			switch {
			case strings.HasPrefix(next, guidanceRequiredPrefix):
				required = guidanceParams(strings.TrimPrefix(next, guidanceRequiredPrefix))
			case strings.HasPrefix(next, guidanceAlternativesPrefix):
				alternatives = guidanceParams(strings.TrimPrefix(next, guidanceAlternativesPrefix))
			default:
				return required, alternatives
			}
		}
		return required, alternatives
	}
	t.Fatalf("getting-started guidance has no entry for %s:\n%s", tool, guidance)
	return nil, nil
}

func guidanceParams(text string) []string {
	matches := guidanceParamPattern.FindAllStringSubmatch(text, -1)
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match[1])
	}
	return names
}

// guidedArguments builds ToolArguments from names and values.
// It fails the test if values omits a name or the helper does not support it.
func guidedArguments(t *testing.T, values map[string]string, names []string) datagen.ToolArguments {
	t.Helper()
	var arguments datagen.ToolArguments
	for _, name := range names {
		value, ok := values[name]
		if !ok {
			t.Fatalf("guidance requires %s, which the test has no value for", name)
		}
		switch name {
		case "workspace_reference":
			arguments.WorkspaceReference = value
		case "project_reference":
			arguments.ProjectReference = value
		case "epic_reference":
			arguments.EpicReference = value
		case "issue_reference":
			arguments.IssueReference = value
		case "name":
			arguments.Name = value
		default:
			t.Fatalf("guidance names %s, which the test cannot set", name)
		}
	}
	return arguments
}

// printedReference extracts the reference from a create confirmation.
// It fails the test if confirmation does not match createdReferencePattern.
func printedReference(t *testing.T, confirmation string) string {
	t.Helper()
	match := createdReferencePattern.FindStringSubmatch(confirmation)
	if match == nil {
		t.Fatalf("create confirmation has no reference:\n%s", confirmation)
	}
	return match[1]
}
