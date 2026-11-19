package integration_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
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
		{
			ActorID:           uuid.Nil,
			Class:             ops.RepairClassReferenceProperty,
			ConfirmationToken: "token",
			NodeID:            issue.ID,
			Profile:           legacyStatusProfile(),
		},
		{
			ActorID:           uuid.New(),
			Class:             ops.RepairClassReferenceProperty,
			ConfirmationToken: "   ",
			NodeID:            issue.ID,
			Profile:           legacyStatusProfile(),
		},
		{
			ActorID:           uuid.New(),
			Class:             ops.RepairClassReferenceProperty,
			ConfirmationToken: "wrong",
			NodeID:            issue.ID,
			Profile:           legacyStatusProfile(),
		},
	}

	for position, want := range []error{
		domain.ErrInvalidArgument,
		domain.ErrInvalidArgument,
		domain.ErrFailedPrecondition,
	} {
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
	input := ops.RepairPreviewInput{
		Class:   ops.RepairClassReferenceProperty,
		NodeID:  issue.ID,
		Profile: legacyStatusProfile(),
	}
	preview, err := f.console.Preview(f.env.Ctx, input)
	if err != nil || !preview.CanApply || preview.ChosenCandidate == nil ||
		preview.ChosenCandidate.NodeID != doneID {
		t.Fatalf("preview = %+v, %v, want the Done state %s", preview, err, doneID)
	}

	result, err := f.console.Apply(
		f.env.Ctx,
		ops.RepairApplyInput{
			ActorID:           uuid.New(),
			Class:             input.Class,
			ConfirmationToken: preview.ConfirmationToken,
			NodeID:            issue.ID,
			Profile:           input.Profile,
		},
	)
	if err != nil || !result.Applied {
		t.Fatalf("Apply = %+v, %v, want an applied repair", result, err)
	}

	repaired := storedStoreView(t, f.env.Stores, issue.ID)
	if _, kept := repaired.Props["status"]; kept || uuidProp(repaired.Props, "state_id") != doneID {
		t.Fatalf(
			"repaired issue props = %v, want state_id %s without status",
			repaired.Props,
			doneID,
		)
	}

	indexed, err := f.env.Stores.Nodes.ListByProperty(
		f.env.Ctx,
		f.env.OrgID,
		"issue",
		"state_id",
		quoted(doneID.String()),
	)
	if err != nil || len(indexed) != 1 || indexed[0].ID != issue.ID {
		t.Fatalf("state_id index lists %v, %v, want issue %s", indexed, err, issue.ID)
	}
}
