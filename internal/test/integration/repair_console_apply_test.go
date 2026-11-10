package integration_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
)

// TestRepairConsoleApplyChecksConfirmation tests reference property repair
// attempts without an actor, with a blank token, and with a wrong token.
// Attempts without an actor or with a blank token must fail with
// domain.ErrInvalidArgument. An attempt with a wrong token must fail with
// domain.ErrFailedPrecondition. Each attempt must leave the issue unchanged.
func TestRepairConsoleApplyChecksConfirmation(t *testing.T) {
	f := newRepairConsoleFixture(t)
	issue := f.legacyIssue(t, map[string]json.RawMessage{"status": quoted("Done")})
	inputs := []ops.RepairApplyInput{
		{ActorID: uuid.Nil, Class: ops.RepairClassReferenceProperty, ConfirmationToken: "token", NodeID: issue.ID, Profile: legacyStatusProfile()},
		{ActorID: uuid.New(), Class: ops.RepairClassReferenceProperty, ConfirmationToken: "   ", NodeID: issue.ID, Profile: legacyStatusProfile()},
		{ActorID: uuid.New(), Class: ops.RepairClassReferenceProperty, ConfirmationToken: "wrong", NodeID: issue.ID, Profile: legacyStatusProfile()},
	}
	for position, want := range []error{domain.ErrInvalidArgument, domain.ErrInvalidArgument, domain.ErrFailedPrecondition} {
		if _, err := f.console.Apply(f.env.Ctx, inputs[position]); !errors.Is(err, want) {
			t.Fatalf("Apply %d returned %v, want an error that wraps %v", position, err, want)
		}
	}
	after := storedStoreView(t, f.env.Stores, issue.ID)
	if !after.UpdatedAt.Equal(issue.UpdatedAt) || string(after.Props["status"]) != `"Done"` {
		t.Fatalf("a refused repair changed issue %s: %+v", issue.ID, after)
	}
}

// TestRepairConsoleApplyResolvesHighestRankState tests state_id repair for an
// issue with legacy status Done. During preview, the console must select the
// Done state over the stored Todo state by sort_order. The repair must store
// the Done state, remove status, and index the issue under the new state_id.
func TestRepairConsoleApplyResolvesHighestRankState(t *testing.T) {
	f := newRepairConsoleFixture(t)
	doneID := stateByName(t, f.env, f.projectID, "Done")
	issue := f.legacyIssue(t, map[string]json.RawMessage{"status": quoted("Done")})
	input := ops.RepairPreviewInput{Class: ops.RepairClassReferenceProperty, NodeID: issue.ID, Profile: legacyStatusProfile()}
	preview, err := f.console.Preview(f.env.Ctx, input)
	if err != nil || !preview.CanApply || preview.ChosenCandidate == nil || preview.ChosenCandidate.NodeID != doneID {
		t.Fatalf("preview = %+v, %v, want the Done state %s", preview, err, doneID)
	}

	result, err := f.console.Apply(f.env.Ctx, ops.RepairApplyInput{
		ActorID: uuid.New(), Class: input.Class, ConfirmationToken: preview.ConfirmationToken, NodeID: issue.ID, Profile: input.Profile,
	})
	if err != nil || !result.Applied {
		t.Fatalf("Apply = %+v, %v, want an applied repair", result, err)
	}
	repaired := storedStoreView(t, f.env.Stores, issue.ID)
	if _, kept := repaired.Props["status"]; kept || uuidProp(repaired.Props, "state_id") != doneID {
		t.Fatalf("repaired issue props = %v, want state_id %s without status", repaired.Props, doneID)
	}
	indexed, err := f.env.Stores.Nodes.ListByProperty(f.env.Ctx, f.env.OrgID, "issue", "state_id", quoted(doneID.String()))
	if err != nil || len(indexed) != 1 || indexed[0].ID != issue.ID {
		t.Fatalf("state_id index lists %v, %v, want issue %s", indexed, err, issue.ID)
	}
}

// TestRepairConsoleApplyMovesParentByName tests parent repair for an issue
// using the legacy parent_epic_title property. The repair must set parent_id
// to the matching epic, replace the child_of edge to the project with one edge
// to the epic created by the repair actor, and remove parent_epic_title.
func TestRepairConsoleApplyMovesParentByName(t *testing.T) {
	f := newRepairConsoleFixture(t)
	epic := createSchemaNode(t, f.env, f.projectID, f.projectID, "epic", nil)
	issue := f.legacyIssue(t, map[string]json.RawMessage{"parent_epic_title": quoted(epic.Name)})
	input := ops.RepairPreviewInput{Class: ops.RepairClassParentReference, NodeID: issue.ID, Profile: parentProfile("epic", "name", "parent_epic_title")}
	preview, err := f.console.Preview(f.env.Ctx, input)
	if err != nil || !preview.CanApply || uuidProp(preview.PlannedProps, "parent_id") != epic.ID {
		t.Fatalf("preview = %+v, %v, want parent_id %s", preview, err, epic.ID)
	}

	actorID := uuid.New()
	if _, err := f.console.Apply(f.env.Ctx, ops.RepairApplyInput{
		ActorID: actorID, Class: input.Class, ConfirmationToken: preview.ConfirmationToken, NodeID: issue.ID, Profile: input.Profile,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	repaired := storedStoreView(t, f.env.Stores, issue.ID)
	if _, kept := repaired.Props["parent_epic_title"]; kept || uuidProp(repaired.Props, "parent_id") != epic.ID {
		t.Fatalf("repaired issue props = %v, want parent_id %s without parent_epic_title", repaired.Props, epic.ID)
	}
	edges, err := f.env.Stores.Relationships.ListBySource(f.env.Ctx, f.env.OrgID, issue.ID, node.RelChildOf)
	if err != nil || len(edges) != 1 || edges[0].TargetID != epic.ID || edges[0].CreatedBy != actorID {
		t.Fatalf("child_of edges of %s = %v, %v, want one edge to %s by %s", issue.ID, edges, err, epic.ID, actorID)
	}
}

// TestRepairConsoleApplyAddsMissingParentEdge tests repair of an issue with
// legacy parent_epic_title, parent_id equal to the epic ID, and a child_of
// edge to the project. During preview, the console must plan a child_of edge
// addition to the epic without a parent_id change. The repair must leave
// exactly one child_of edge to the epic.
func TestRepairConsoleApplyAddsMissingParentEdge(t *testing.T) {
	f := newRepairConsoleFixture(t)
	epic := createSchemaNode(t, f.env, f.projectID, f.projectID, "epic", nil)
	issue := f.legacyIssue(t, map[string]json.RawMessage{
		"parent_epic_title": quoted(epic.Name), "parent_id": quoted(epic.ID.String()),
	})
	input := ops.RepairPreviewInput{Class: ops.RepairClassParentReference, NodeID: issue.ID, Profile: parentProfile("epic", "name", "parent_epic_title")}
	preview, err := f.console.Preview(f.env.Ctx, input)
	if err != nil || !preview.CanApply {
		t.Fatalf("preview = %+v, %v, want an applicable repair", preview, err)
	}
	if _, planned := preview.PlannedProps["parent_id"]; planned || string(preview.PlannedProps["parent_epic_title"]) != "null" {
		t.Fatalf("planned props = %v, want only the parent_epic_title removal", preview.PlannedProps)
	}
	if !plansParentEdge(preview.PlannedRelationships, epic.ID) {
		t.Fatalf("planned relationships = %+v, want an added child_of edge to %s", preview.PlannedRelationships, epic.ID)
	}

	if _, err := f.console.Apply(f.env.Ctx, ops.RepairApplyInput{
		ActorID: uuid.New(), Class: input.Class, ConfirmationToken: preview.ConfirmationToken, NodeID: issue.ID, Profile: input.Profile,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	requireSingleParentEdge(t, f.env, issue.ID, epic.ID)
}

// plansParentEdge reports whether changes add a child_of edge to targetID.
func plansParentEdge(changes []ops.RepairRelationshipChange, targetID uuid.UUID) bool {
	for _, change := range changes {
		if change.Action == ops.RepairRelationshipAdd && change.TargetID == targetID {
			return true
		}
	}
	return false
}
