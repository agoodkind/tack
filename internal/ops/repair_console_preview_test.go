package ops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func TestPreviewStrayAliasStateRejectsMissingNodeID(t *testing.T) {
	console := NewRepairConsole(&repairNodeRepo{}, &repairReader{}, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})
	_, err := console.Preview(context.Background(), RepairPreviewInput{Class: RepairClassStrayAliasState})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Preview err = %v want ErrInvalidArgument", err)
	}
}

func TestPreviewStrayAliasStateReturnsNoopWhenRawStateMissing(t *testing.T) {
	issueID := uuid.New()
	updatedAt := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	reader := &repairReader{views: map[uuid.UUID]*node.NodeView{
		issueID: {ID: issueID, OrgID: uuid.New(), NodeType: "issue", Name: "Issue", UpdatedAt: updatedAt},
	}}
	console := NewRepairConsole(&repairNodeRepo{reader: reader}, reader, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})

	preview, err := console.Preview(context.Background(), RepairPreviewInput{Class: RepairClassStrayAliasState, NodeID: issueID})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if preview.NeedsRepair {
		t.Fatal("Preview NeedsRepair = true want false")
	}
	if preview.CanApply {
		t.Fatal("Preview CanApply = true want false")
	}
	if preview.Summary != "raw state alias is absent" {
		t.Fatalf("Preview Summary = %q", preview.Summary)
	}
}

func TestPreviewStrayAliasStateRejectsMissingScope(t *testing.T) {
	issueID := uuid.New()
	reader := &repairReader{views: map[uuid.UUID]*node.NodeView{
		issueID: {
			ID: issueID, OrgID: uuid.New(), NodeType: "issue", Name: "Issue",
			UpdatedAt: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
			Props:     map[string]json.RawMessage{"state": mustRaw(t, "Done")},
		},
	}}
	console := NewRepairConsole(&repairNodeRepo{reader: reader}, reader, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})

	_, err := console.Preview(context.Background(), RepairPreviewInput{Class: RepairClassStrayAliasState, NodeID: issueID})
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("Preview err = %v want ErrFailedPrecondition", err)
	}
}

func TestPreviewStrayAliasStateDisablesApplyForAmbiguousWorkflowState(t *testing.T) {
	orgID := uuid.New()
	projectID := uuid.New()
	issueID := uuid.New()
	firstStateID := uuid.New()
	secondStateID := uuid.New()
	reader := &repairReader{
		views: map[uuid.UUID]*node.NodeView{
			issueID: {
				ID: issueID, OrgID: orgID, NodeType: "issue", Name: "Issue",
				UpdatedAt: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
				Props: map[string]json.RawMessage{
					"scope_id":  mustRaw(t, projectID.String()),
					"parent_id": mustRaw(t, projectID.String()),
					"state":     mustRaw(t, "Done"),
				},
			},
		},
		stateViews: []*node.NodeView{
			stateView(t, firstStateID, orgID, projectID, "Done", 2),
			stateView(t, secondStateID, orgID, projectID, "Done", 3),
		},
	}
	console := NewRepairConsole(&repairNodeRepo{reader: reader}, reader, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})

	preview, err := console.Preview(context.Background(), RepairPreviewInput{Class: RepairClassStrayAliasState, NodeID: issueID})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !preview.NeedsRepair {
		t.Fatal("Preview NeedsRepair = false want true")
	}
	if preview.CanApply {
		t.Fatal("Preview CanApply = true want false")
	}
	if !strings.Contains(preview.Summary, "matched multiple nodes") {
		t.Fatalf("Preview Summary = %q want ambiguous-state detail", preview.Summary)
	}
	if preview.ConfirmationToken != "" {
		t.Fatalf("Preview ConfirmationToken = %q want empty", preview.ConfirmationToken)
	}
}
