package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"goodkind.io/tack/internal/ops"
)

// TestRepairConsolePreviewTransformsLegacyProperties tests a property
// transform preview for an issue with legacy properties Description, status,
// and progress. The console must plan a description with the text of
// Description and the status under its heading. It must also plan the removal
// of each legacy property.
func TestRepairConsolePreviewTransformsLegacyProperties(t *testing.T) {
	f := newRepairConsoleFixture(t)
	issue := f.legacyIssue(
		t,
		map[string]json.RawMessage{
			"Description": quoted("Imported description"),
			"status":      quoted("Completed"),
			"progress":    quoted("100%"),
		},
	)
	profile := &ops.RepairReferenceProfile{
		Name:         "legacy-props",
		RemoveFields: []string{"progress"},
		RenameFields: []ops.RepairRenameField{{From: "Description", To: "description"}},
		AppendTextFields: []ops.RepairAppendTextField{
			{From: "status", To: "description", Heading: "Imported status"},
		},
	}

	preview, err := f.console.Preview(
		f.env.Ctx,
		ops.RepairPreviewInput{
			Class:   ops.RepairClassPropsTransform,
			NodeID:  issue.ID,
			Profile: profile,
		},
	)
	if err != nil || !preview.CanApply {
		t.Fatalf("preview = %+v, %v, want an applicable transform", preview, err)
	}

	var description string
	if err := json.Unmarshal(preview.PlannedProps["description"], &description); err != nil {
		t.Fatalf("decode the planned description %s: %v", preview.PlannedProps["description"], err)
	}

	if !strings.Contains(description, "Imported description") ||
		!strings.Contains(description, "Imported status\nCompleted") {
		t.Fatalf(
			"planned description = %q, want the renamed text and the appended status",
			description,
		)
	}

	for _, field := range []string{"Description", "status", "progress"} {
		if string(preview.PlannedProps[field]) != "null" {
			t.Fatalf("planned %s = %s, want null", field, preview.PlannedProps[field])
		}
	}
}
