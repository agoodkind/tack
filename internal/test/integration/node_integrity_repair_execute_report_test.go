package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityRepairExecuteReportComment executes a report entry for an
// issue with a narrative progress property and a legacy status property. The
// execution must create one comment under the issue with the complete report,
// verify it, and then remove both properties. A second execution must not
// create a second comment.
func TestNodeIntegrityRepairExecuteReportComment(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	report := strings.Repeat("The rollout finished on every guest. ", 30)
	issueID := createRepairIssue(t, env, graph.projectID, "Issue with a progress report")
	setLegacyProps(
		t,
		env,
		issueID,
		map[string]json.RawMessage{"progress": textValue(report), "status": textValue("Done")},
	)
	entry := reportEntry(
		"tack-582-report",
		issueID,
		report,
		map[string]json.RawMessage{
			"progress": json.RawMessage("null"),
			"status":   json.RawMessage("null"),
		},
	)
	manifest := freezeRepairRequest(t, repairRequest(entry))

	applied, err := executeRepairManifest(t, manifest)
	if err != nil {
		t.Fatalf("execute the report entry: %v", err)
	}

	outcome := requireApplyStatus(t, applied, entry.RepairID, ops.RepairApplyApplied)
	want := []node.RepairPhase{
		node.RepairPhaseCommentCreated,
		node.RepairPhaseCommentVerified,
		node.RepairPhaseCompleted,
	}
	if strings.Join(phaseNames(outcome.Committed), ",") != strings.Join(phaseNames(want), ",") {
		t.Fatalf("committed phases = %v, want %v", outcome.Committed, want)
	}

	comment := requireReportComment(t, env, entry, report)
	checkpoint, err := env.Stores.Repairs().RepairCheckpoint(env.Ctx, entry.RepairID)
	if err != nil || checkpoint == nil || checkpoint.Phase != node.RepairPhaseCompleted ||
		checkpoint.CommentID != comment.ID {
		t.Fatalf(
			"checkpoint = %+v, %v, want completed with comment %s",
			checkpoint,
			err,
			comment.ID,
		)
	}

	view, err := env.Stores.Views.Get(env.Ctx, issueID)
	if err != nil || view == nil {
		t.Fatalf("read issue %s: %v", issueID, err)
	}

	for _, removed := range []string{"progress", "status"} {
		if _, present := view.Props[removed]; present {
			t.Fatalf("issue %s still has property %s", issueID, removed)
		}
	}

	requireUnchangedRerun(t, manifest)
	requireReportComment(t, env, entry, report)
}

// TestNodeIntegrityRepairExecuteReportRetry executes a report entry with a
// result that keeps an undeclared legacy property. The execution must create
// and verify the report comment, refuse the result, and keep the progress
// property. A second execution must resume at the final phase, refuse the
// result again, and keep exactly one report comment.
func TestNodeIntegrityRepairExecuteReportRetry(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	report := "The migration stopped after the second batch."
	issueID := createRepairIssue(t, env, graph.projectID, "Issue with an invalid result")
	setLegacyProps(
		t,
		env,
		issueID,
		map[string]json.RawMessage{"progress": textValue(report), "status": textValue("open")},
	)
	entry := reportEntry(
		"tack-582-report-retry",
		issueID,
		report,
		map[string]json.RawMessage{"progress": json.RawMessage("null")},
	)
	manifest := freezeRepairRequest(t, repairRequest(entry))

	for attempt := 1; attempt <= 2; attempt++ {
		applied, err := executeRepairManifest(t, manifest)
		if err == nil {
			t.Fatalf("attempt %d: execute an invalid result succeeded", attempt)
		}

		outcome := requireApplyStatus(t, applied, entry.RepairID, ops.RepairApplyFailed)
		if outcome.NextPhase != node.RepairPhaseCompleted {
			t.Fatalf("attempt %d: next phase = %q, want completed", attempt, outcome.NextPhase)
		}

		requireCheckpointPhase(t, env, entry.RepairID, node.RepairPhaseCommentVerified)
		requireReportComment(t, env, entry, report)
		view, err := env.Stores.Views.Get(env.Ctx, issueID)
		if err != nil || view == nil || propText(t, view.Props, "progress") != report {
			t.Fatalf(
				"attempt %d: issue %s = %+v, %v, want the progress property",
				attempt,
				issueID,
				view,
				err,
			)
		}
	}
}
