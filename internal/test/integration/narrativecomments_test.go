package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// reportEntry returns an executable entry that preserves the progress
// property of issueID in a comment and applies props to the issue.
func reportEntry(
	repairID string,
	issueID uuid.UUID,
	report string,
	props map[string]json.RawMessage,
) ops.NodeIntegrityEntry {
	return ops.NodeIntegrityEntry{
		RepairID: repairID,
		NodeID:   issueID,
		NodeType: "issue",
		Status:   ops.RepairEntryExecutable,
		Classes:  []string{"narrative_progress"},
		Evidence: []ops.RepairEvidence{
			{
				Kind:      ops.RepairEvidenceObserved,
				Reference: "TACK-582 narrative reports",
				Summary:   "The progress property contains a narrative report.",
			},
		},
		Result: ops.RepairResult{Props: props},
		Report: &ops.RepairReport{
			SourceProperty: "progress",
			NodeType:       "comment",
			Name:           "Progress report",
			Props: map[string]json.RawMessage{
				"description": textValue("Original progress report:\n\n" + report),
			},
		},
	}
}

// requireReportComment requires exactly one comment under entry.NodeID and
// returns its view. The comment must use entry.NodeID as its parent_id and its
// only child_of target. The comment must match the node type and name in
// entry.Report and store every property in entry.Report.Props with its manifest
// value. The comment description must contain the complete report text.
func requireReportComment(
	t *testing.T,
	env *integration.TestEnv,
	entry ops.NodeIntegrityEntry,
	report string,
) *node.NodeView {
	t.Helper()
	comments := commentsUnder(t, env, entry.NodeID)
	if len(comments) != 1 {
		t.Fatalf("node %s has %d comments, want one report comment", entry.NodeID, len(comments))
	}

	comment := requireParent(t, env, comments[0].ID, entry.NodeID)
	if comment.NodeType != entry.Report.NodeType || comment.Name != entry.Report.Name {
		t.Fatalf(
			"report comment = %s %q, want %s %q",
			comment.NodeType,
			comment.Name,
			entry.Report.NodeType,
			entry.Report.Name,
		)
	}

	for name := range entry.Report.Props {
		if got, want := propText(t, comment.Props, name), propText(
			t,
			entry.Report.Props,
			name,
		); got != want {
			t.Fatalf("report comment %s = %q, want %q", name, got, want)
		}
	}

	if description := propText(t, comment.Props, "description"); !strings.Contains(
		description,
		report,
	) {
		t.Fatalf("report comment description = %q, want the complete report", description)
	}

	return comment
}

func phaseNames(phases []node.RepairPhase) []string {
	names := make([]string, 0, len(phases))
	for _, phase := range phases {
		names = append(names, string(phase))
	}

	return names
}
