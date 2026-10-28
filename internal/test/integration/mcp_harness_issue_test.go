package integration

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
)

var confirmationReferencePattern = regexp.MustCompile("- Reference: `([^`]+)`")

// CreateIssue creates one issue in the harness's project and returns the
// reference printed in the create confirmation.
func (h *MCPHarness) CreateIssue(t *testing.T, name string) string {
	t.Helper()
	reference, err := h.TryCreateIssue(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	return reference
}

// TryCreateIssue creates an issue in the harness project, registers the node
// in h.titles, and returns its printed reference. It returns an error instead
// of failing the test and supports concurrent calls.
func (h *MCPHarness) TryCreateIssue(ctx context.Context, name string) (string, error) {
	arguments := h.projectArgs()
	arguments.Name = name
	result, err := h.driver.Call(ctx, h.token, "tack_create_issue", arguments)
	if err != nil {
		return "", fmt.Errorf("call tack_create_issue for %q: %w", name, err)
	}
	text := result.Text()
	match := confirmationReferencePattern.FindStringSubmatch(text)
	if match == nil {
		return "", fmt.Errorf("create issue confirmation for %q has no reference:\n%s", name, text)
	}
	nodeID, err := uuid.Parse(result.RawID())
	if err != nil {
		return "", fmt.Errorf("parse the created issue ID of %q: %w", name, err)
	}
	h.titles.Add(result.SearchItem(), nodeID)
	return match[1], nil
}

// SequenceChanges returns the change events of the sequence counter stored
// under counterKey in the harness organization from the FoundationDB outbox.
func (h *MCPHarness) SequenceChanges(ctx context.Context, counterKey string) ([]audit.Event, error) {
	return harnessOutboxMatching(ctx, h, "sequence counter "+counterKey, func(event audit.Event) bool {
		return event.Entity.NodeType == audit.TargetSequenceCounter && event.Entity.Name == counterKey &&
			event.Context.OrgID == h.orgID && event.Context.Phase == audit.PhaseChange
	})
}
