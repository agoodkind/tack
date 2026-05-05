package ops

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func TestPreviewStrayAliasStateChoosesHigherWorkflowState(t *testing.T) {
	orgID := uuid.New()
	projectID := uuid.New()
	issueID := uuid.New()
	inProgressID := uuid.New()
	doneID := uuid.New()
	updatedAt := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	reader := &repairReader{
		views: map[uuid.UUID]*node.NodeView{
			issueID: {
				ID: issueID, OrgID: orgID, NodeType: "issue", Name: "Issue", UpdatedAt: updatedAt,
				Props: map[string]json.RawMessage{
					"scope_id":  mustRaw(t, projectID.String()),
					"parent_id": mustRaw(t, projectID.String()),
					"state":     mustRaw(t, "Done"),
					"state_id":  mustRaw(t, inProgressID.String()),
				},
			},
		},
		stateViews: []*node.NodeView{
			stateView(t, inProgressID, orgID, projectID, "In Progress", 2),
			stateView(t, doneID, orgID, projectID, "Done", 3),
		},
	}
	console := NewRepairConsole(&repairNodeRepo{reader: reader}, reader, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})

	preview, err := console.Preview(context.Background(), RepairPreviewInput{Class: RepairClassStrayAliasState, NodeID: issueID})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !preview.CanApply {
		t.Fatalf("Preview CanApply = false, summary=%q", preview.Summary)
	}
	if preview.WinnerStateID != doneID {
		t.Fatalf("WinnerStateID = %s want %s", preview.WinnerStateID, doneID)
	}
	if preview.ResolvedCanonicalStateID != inProgressID {
		t.Fatalf("ResolvedCanonicalStateID = %s want %s", preview.ResolvedCanonicalStateID, inProgressID)
	}
}

func TestApplyStrayAliasStateRequiresMatchingConfirmationToken(t *testing.T) {
	orgID := uuid.New()
	projectID := uuid.New()
	issueID := uuid.New()
	stateID := uuid.New()
	updatedAt := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	reader := &repairReader{
		views: map[uuid.UUID]*node.NodeView{
			issueID: {
				ID: issueID, OrgID: orgID, NodeType: "issue", Name: "Issue", UpdatedAt: updatedAt,
				Props: map[string]json.RawMessage{
					"scope_id":  mustRaw(t, projectID.String()),
					"parent_id": mustRaw(t, projectID.String()),
					"state":     mustRaw(t, "Done"),
				},
			},
		},
		stateViews: []*node.NodeView{stateView(t, stateID, orgID, projectID, "Done", 3)},
	}
	nodeRepo := &repairNodeRepo{reader: reader}
	console := NewRepairConsole(nodeRepo, reader, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})

	preview, err := console.Preview(context.Background(), RepairPreviewInput{Class: RepairClassStrayAliasState, NodeID: issueID})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	_, err = console.Apply(context.Background(), RepairApplyInput{ActorID: uuid.New(), Class: RepairClassStrayAliasState, NodeID: issueID, ConfirmationToken: "wrong"})
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("Apply err = %v want ErrFailedPrecondition", err)
	}
	if nodeRepo.updatedNode != nil {
		t.Fatal("Apply wrote despite mismatched token")
	}
	if preview.ConfirmationToken == "" {
		t.Fatal("Preview confirmation token is empty")
	}
}

func TestApplyStrayAliasStateRemovesAliasAndUpdatesCanonical(t *testing.T) {
	orgID := uuid.New()
	projectID := uuid.New()
	issueID := uuid.New()
	inProgressID := uuid.New()
	doneID := uuid.New()
	updatedAt := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	reader := &repairReader{
		views: map[uuid.UUID]*node.NodeView{
			projectID: {ID: projectID, OrgID: orgID, NodeType: "project", Name: "Project", Props: map[string]json.RawMessage{"identifier": mustRaw(t, "TACK")}},
			issueID: {
				ID: issueID, OrgID: orgID, NodeType: "issue", Name: "Issue", UpdatedAt: updatedAt,
				Props: map[string]json.RawMessage{
					"scope_id":  mustRaw(t, projectID.String()),
					"parent_id": mustRaw(t, projectID.String()),
					"state":     mustRaw(t, "TACK::Done"),
					"state_id":  mustRaw(t, inProgressID.String()),
				},
			},
		},
		stateViews: []*node.NodeView{
			stateView(t, inProgressID, orgID, projectID, "In Progress", 2),
			stateView(t, doneID, orgID, projectID, "Done", 3),
		},
	}
	nodeRepo := &repairNodeRepo{reader: reader}
	searcher := &repairSearcher{}
	console := NewRepairConsole(nodeRepo, reader, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, searcher)

	preview, err := console.Preview(context.Background(), RepairPreviewInput{Class: RepairClassStrayAliasState, NodeID: issueID})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	result, err := console.Apply(context.Background(), RepairApplyInput{ActorID: uuid.New(), Class: RepairClassStrayAliasState, NodeID: issueID, ConfirmationToken: preview.ConfirmationToken})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Applied {
		t.Fatal("Apply Applied = false")
	}
	if _, ok := nodeRepo.updatedNode.Props["state"]; ok {
		t.Fatal("updated node still contains raw state alias")
	}
	if got := rawUUIDProp(nodeRepo.updatedNode.Props, "state_id"); got != doneID {
		t.Fatalf("updated state_id = %s want %s", got, doneID)
	}
	if searcher.indexCount == 0 {
		t.Fatal("Apply did not reindex search document")
	}
}
