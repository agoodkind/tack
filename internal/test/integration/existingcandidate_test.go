package integration_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/ops"
)

func TestRepairConsoleUsesExistingParentRelationship(t *testing.T) {
	fixture := newRepairConsoleFixture(t)
	issue := fixture.legacyIssue(
		t,
		map[string]json.RawMessage{"legacy_project": quoted("missing project")},
	)
	profile := parentProfile("project", "name", "legacy_project")
	profile.ConflictPolicy = ops.RepairConflictPreferExisting
	input := ops.RepairPreviewInput{
		Class:   ops.RepairClassParentReference,
		NodeID:  issue.ID,
		Profile: profile,
	}
	preview, err := fixture.console.Preview(fixture.env.Ctx, input)
	if err != nil {
		t.Fatalf("preview existing parent: %v", err)
	}

	if preview.ChosenCandidate == nil || preview.ChosenCandidate.NodeID != fixture.projectID ||
		!preview.CanApply {
		t.Fatalf("preview = %+v, want existing project %s", preview, fixture.projectID)
	}

	before := storedStoreView(t, fixture.env.Stores, issue.ID)
	_, err = fixture.console.Apply(
		fixture.env.Ctx,
		ops.RepairApplyInput{
			ActorID:           uuid.New(),
			Class:             input.Class,
			NodeID:            issue.ID,
			Profile:           profile,
			ConfirmationToken: preview.ConfirmationToken,
		},
	)
	if err != nil {
		t.Fatalf("apply existing parent: %v", err)
	}

	stored := storedStoreView(t, fixture.env.Stores, issue.ID)
	if !bytes.Equal(stored.Props["parent_id"], before.Props["parent_id"]) {
		t.Fatalf("repair changed the stored parent property for issue %s", issue.ID)
	}

	if _, exists := stored.Props["legacy_project"]; exists {
		t.Fatal("repair did not remove legacy_project")
	}

	requireSingleParentEdge(t, fixture.env, issue.ID, fixture.projectID)
}
