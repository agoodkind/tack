package integration_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
)

func TestRepairConsolePreviewsExplicitProjectWithAmbiguousParents(t *testing.T) {
	f := newRepairConsoleFixture(t)
	other := createSchemaNode(t, f.env, f.projectID, f.projectID, "epic", nil)
	issue := f.legacyIssue(t, map[string]json.RawMessage{
		"requested_project": quoted(f.projectID.String()),
		"scope_id":          json.RawMessage("null"),
	})
	if err := f.env.Stores.Relationships.Add(f.env.Ctx, &node.Relationship{
		OrgID:        f.env.OrgID,
		SourceID:     issue.ID,
		RelationType: node.RelChildOf,
		TargetID:     other.ID,
		CreatedBy:    uuid.New(),
		CreatedAt:    time.Now().UTC(),
		Props:        nil,
	}); err != nil {
		t.Fatalf("add second parent of %s: %v", issue.ID, err)
	}

	profile := parentProfile("project", "", "requested_project")
	profile.ScopeFields = nil
	input := ops.RepairPreviewInput{
		Class:   ops.RepairClassParentReference,
		NodeID:  issue.ID,
		Profile: profile,
	}
	preview, err := f.console.Preview(f.env.Ctx, input)
	if err != nil {
		t.Fatalf("preview explicit project parent: %v", err)
	}

	if !preview.CanApply || preview.ChosenCandidate == nil ||
		preview.ChosenCandidate.NodeID != f.projectID {
		t.Fatalf("preview = %+v, want project %s", preview, f.projectID)
	}

	if len(preview.PlannedRelationships) != 1 ||
		preview.PlannedRelationships[0].Action != ops.RepairRelationshipRemove ||
		preview.PlannedRelationships[0].TargetID != other.ID {
		t.Fatalf("parent changes = %+v, want removal of %s", preview.PlannedRelationships, other.ID)
	}

	profile.Normalization.ValueMap = map[string]string{f.projectID.String(): "main::project"}
	if _, err := f.console.Preview(f.env.Ctx, input); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("preview qualified project with ambiguous parents: %v, want conflict", err)
	}
}
