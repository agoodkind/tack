package integration_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/ops"
)

// unresolvedSummary is the preview summary when a reference lookup does not
// resolve any candidate.
const unresolvedSummary = "no reference candidate resolved"

// TestRepairConsolePreviewWithoutRepair tests repair previews. A preview
// without a node ID must fail with domain.ErrInvalidArgument. An issue
// without legacy status must not need repair. An issue with legacy status
// Done, two Done states, and without state_id must need a repair that the
// console cannot apply.
func TestRepairConsolePreviewWithoutRepair(t *testing.T) {
	f := newRepairConsoleFixture(t)
	_, err := f.console.Preview(f.env.Ctx, ops.RepairPreviewInput{Class: ops.RepairClassReferenceProperty, NodeID: uuid.Nil, Profile: legacyStatusProfile()})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Preview without a node ID returned %v, want an invalid argument", err)
	}
	plain := createSchemaNode(t, f.env, f.projectID, f.projectID, "issue", nil)
	preview, err := f.console.Preview(f.env.Ctx, ops.RepairPreviewInput{Class: ops.RepairClassReferenceProperty, NodeID: plain.ID, Profile: legacyStatusProfile()})
	if err != nil || preview.NeedsRepair || preview.CanApply {
		t.Fatalf("preview of issue %s without status = %+v, %v, want no repair", plain.ID, preview, err)
	}

	duplicate := schemaCreateInput(f.projectID, f.projectID, "state", nil)
	duplicate.Name = "Done"
	if _, err := f.env.NodeSvc.Create(f.env.Ctx, duplicate); err != nil {
		t.Fatalf("create a second Done state: %v", err)
	}
	ambiguous := f.legacyIssue(t, map[string]json.RawMessage{"status": quoted("Done"), "state_id": json.RawMessage("null")})
	preview, err = f.console.Preview(f.env.Ctx, ops.RepairPreviewInput{Class: ops.RepairClassReferenceProperty, NodeID: ambiguous.ID, Profile: legacyStatusProfile()})
	if err != nil || !preview.NeedsRepair || preview.CanApply || !strings.Contains(preview.Summary, unresolvedSummary) {
		t.Fatalf("preview of ambiguous issue %s = %+v, %v, want a blocked repair", ambiguous.ID, preview, err)
	}
}

// TestRepairConsolePreviewMatchesByReferenceStrategy tests parent repair
// previews using the target type's reference strategy. The console must
// resolve the workspace by its slug. It must not resolve a state by a legacy
// slug property when matching by scoped name. It must resolve the first epic
// of the project by the scoped sequence 1.
func TestRepairConsolePreviewMatchesByReferenceStrategy(t *testing.T) {
	f := newRepairConsoleFixture(t)
	epic := createSchemaNode(t, f.env, f.projectID, f.projectID, "epic", nil)
	state := createSchemaNode(t, f.env, f.projectID, f.projectID, "state", nil)
	f.legacyNode(t, state, map[string]json.RawMessage{"slug": quoted("ready-slug")})
	workspaceID := uuidProp(storedStoreView(t, f.env.Stores, f.projectID).Props, "parent_id")
	issue := f.legacyIssue(t, map[string]json.RawMessage{
		"legacy_workspace": quoted("main"), "legacy_state": quoted("ready-slug"), "legacy_epic": quoted("1"),
	})

	for _, expected := range []struct {
		typeKey, source string
		parentID        uuid.UUID
	}{
		{"workspace", "legacy_workspace", workspaceID},
		{"state", "legacy_state", uuid.Nil},
		{"epic", "legacy_epic", epic.ID},
	} {
		input := ops.RepairPreviewInput{Class: ops.RepairClassParentReference, NodeID: issue.ID, Profile: parentProfile(expected.typeKey, "", expected.source)}
		preview, err := f.console.Preview(f.env.Ctx, input)
		if err != nil {
			t.Fatalf("preview of the %s match: %v", expected.typeKey, err)
		}
		chosen := uuid.Nil
		if preview.ChosenCandidate != nil {
			chosen = preview.ChosenCandidate.NodeID
		}
		if chosen != expected.parentID {
			t.Fatalf("the %s match chose %s (summary %q), want %s", expected.typeKey, chosen, preview.Summary, expected.parentID)
		}
	}
}

// TestRepairConsolePreviewTransformsLegacyProperties tests a property
// transform preview for an issue with legacy properties Description, status,
// and progress. The console must plan a description with the text of
// Description and the status under its heading. It must also plan the removal
// of each legacy property.
func TestRepairConsolePreviewTransformsLegacyProperties(t *testing.T) {
	f := newRepairConsoleFixture(t)
	issue := f.legacyIssue(t, map[string]json.RawMessage{
		"Description": quoted("Imported description"), "status": quoted("Completed"), "progress": quoted("100%"),
	})
	profile := &ops.RepairReferenceProfile{
		Name: "legacy-props", RemoveFields: []string{"progress"},
		RenameFields:     []ops.RepairRenameField{{From: "Description", To: "description"}},
		AppendTextFields: []ops.RepairAppendTextField{{From: "status", To: "description", Heading: "Imported status"}},
	}

	preview, err := f.console.Preview(f.env.Ctx, ops.RepairPreviewInput{Class: ops.RepairClassPropsTransform, NodeID: issue.ID, Profile: profile})
	if err != nil || !preview.CanApply {
		t.Fatalf("preview = %+v, %v, want an applicable transform", preview, err)
	}
	var description string
	if err := json.Unmarshal(preview.PlannedProps["description"], &description); err != nil {
		t.Fatalf("decode the planned description %s: %v", preview.PlannedProps["description"], err)
	}
	if !strings.Contains(description, "Imported description") || !strings.Contains(description, "Imported status\nCompleted") {
		t.Fatalf("planned description = %q, want the renamed text and the appended status", description)
	}
	for _, field := range []string{"Description", "status", "progress"} {
		if string(preview.PlannedProps[field]) != "null" {
			t.Fatalf("planned %s = %s, want null", field, preview.PlannedProps[field])
		}
	}
}
