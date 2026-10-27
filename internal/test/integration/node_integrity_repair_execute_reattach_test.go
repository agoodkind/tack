package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityRepairExecuteReattachesComment executes an entry for a
// duplicate notice comment with an absent parent. The execution must attach
// the comment to the selected issue with one child_of edge. It must preserve
// the comment ID, name, description, creator, and creation time. The repair
// operator must become the last updater of the comment. A second execution
// must not change any record.
func TestNodeIntegrityRepairExecuteReattachesComment(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	original := createRepairIssue(t, env, graph.projectID, "Original parent")
	target := createRepairIssue(t, env, graph.projectID, "Duplicate target")
	commentID := createRepairComment(t, env, original, graph.projectID, "Duplicate of the target issue")
	setLegacyProps(t, env, commentID, map[string]json.RawMessage{"parent_id": textValue(uuid.New().String())})
	if err := env.Stores.Relationships.Remove(env.Ctx, env.OrgID, commentID, node.RelChildOf, original); err != nil {
		t.Fatalf("remove the child_of edge of comment %s: %v", commentID, err)
	}
	before, err := env.Stores.Views.Get(env.Ctx, commentID)
	if err != nil || before == nil {
		t.Fatalf("read comment %s: %v", commentID, err)
	}
	entry := ops.NodeIntegrityEntry{
		RepairID: "tack-582-reattach-notice", NodeID: commentID, NodeType: "comment", Status: ops.RepairEntryExecutable,
		Classes: []string{"absent_parent"},
		Evidence: []ops.RepairEvidence{{
			Kind: ops.RepairEvidenceObserved, Reference: "TACK-582 duplicate notice", Summary: "The comment text identifies the duplicate target.",
		}},
		Result: ops.RepairResult{Props: map[string]json.RawMessage{"parent_id": textValue(target.String())}},
	}
	manifest := freezeRepairRequest(t, repairRequest(entry))

	applied, err := executeRepairManifest(t, manifest)
	if err != nil {
		t.Fatalf("execute the reattachment: %v", err)
	}
	requireApplyStatus(t, applied, entry.RepairID, ops.RepairApplyApplied)
	after := requireParent(t, env, commentID, target)
	if after.ID != before.ID || after.Name != before.Name || after.CreatedBy != before.CreatedBy || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("reattached comment = %+v, want the identity and provenance of %+v", after, before)
	}
	if propText(t, after.Props, "description") != propText(t, before.Props, "description") {
		t.Fatalf("reattached comment description = %s, want %s", after.Props["description"], before.Props["description"])
	}
	if after.UpdatedBy.String() != repairOperatorID {
		t.Fatalf("reattached comment updated_by = %s, want the operator %s", after.UpdatedBy, repairOperatorID)
	}
	if comments := commentsUnder(t, env, target); len(comments) != 1 || comments[0].ID != commentID {
		t.Fatalf("comments under the target = %+v, want only comment %s", comments, commentID)
	}
	requireUnchangedRerun(t, manifest)
}

// TestNodeIntegrityRepairExecuteNormalizesProperties executes an entry that
// selects the Done state, normalizes a priority label to its option key, and
// removes a legacy status property. The issue must store the selected values
// without the status property, and its parent and scope must stay unchanged.
func TestNodeIntegrityRepairExecuteNormalizesProperties(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	issueID := createRepairIssue(t, env, graph.projectID, "Issue with legacy values")
	setLegacyProps(t, env, issueID, map[string]json.RawMessage{"status": textValue("Todo"), "priority": textValue("High")})
	done := projectState(t, env, graph.projectID, "Done")
	entry := ops.NodeIntegrityEntry{
		RepairID: "tack-582-normalize", NodeID: issueID, NodeType: "issue", Status: ops.RepairEntryExecutable,
		Classes: []string{"legacy_status", "select_label"},
		Evidence: []ops.RepairEvidence{{
			Kind: ops.RepairEvidenceDecision, Reference: "TACK-582 legacy states", Summary: "A dated completion comment selects Done.",
		}},
		Result: ops.RepairResult{Props: map[string]json.RawMessage{
			"state_id": textValue(done.String()), "priority": textValue("high"), "status": json.RawMessage("null"),
		}},
	}
	manifest := freezeRepairRequest(t, repairRequest(entry))

	applied, err := executeRepairManifest(t, manifest)
	if err != nil {
		t.Fatalf("execute the normalization: %v", err)
	}
	requireApplyStatus(t, applied, entry.RepairID, ops.RepairApplyApplied)
	view := requireParent(t, env, issueID, graph.projectID)
	if propText(t, view.Props, "state_id") != done.String() || propText(t, view.Props, "priority") != "high" {
		t.Fatalf("issue props = %v, want the Done state and priority high", view.Props)
	}
	if _, present := view.Props["status"]; present {
		t.Fatalf("issue %s still has the legacy status property", issueID)
	}
	if propText(t, view.Props, "scope_id") != graph.projectID.String() {
		t.Fatalf("issue scope_id = %s, want project %s", view.Props["scope_id"], graph.projectID)
	}
	requireUnchangedRerun(t, manifest)
}
