package integration

import (
	"regexp"
	"testing"
)

var confirmationReferencePattern = regexp.MustCompile("- Reference: `([^`]+)`")

// CreateIssue creates one issue in the harness's project and returns the
// reference printed in the create confirmation.
func (h *MCPHarness) CreateIssue(t *testing.T, name string) string {
	t.Helper()
	arguments := h.projectArgs()
	arguments.Name = name
	text := h.Call(t, "tack_create_issue", arguments).Text()
	match := confirmationReferencePattern.FindStringSubmatch(text)
	if match == nil {
		t.Fatalf("create issue confirmation has no reference:\n%s", text)
	}
	return match[1]
}
