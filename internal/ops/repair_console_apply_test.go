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

func TestApplyReferencePropertyRejectsMissingActorID(t *testing.T) {
	console := NewRepairConsole(&repairNodeRepo{}, &repairReader{}, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})
	_, err := console.Apply(context.Background(), RepairApplyInput{Class: RepairClassReferenceProperty, NodeID: uuid.New(), ConfirmationToken: "token", Profile: phaseProfile()})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Apply err = %v want ErrInvalidArgument", err)
	}
}

func TestApplyReferencePropertyRejectsBlankConfirmationToken(t *testing.T) {
	console := NewRepairConsole(&repairNodeRepo{}, &repairReader{}, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})
	_, err := console.Apply(context.Background(), RepairApplyInput{ActorID: uuid.New(), Class: RepairClassReferenceProperty, NodeID: uuid.New(), ConfirmationToken: "   ", Profile: phaseProfile()})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Apply err = %v want ErrInvalidArgument", err)
	}
}

func TestApplyReferencePropertyRequiresMatchingConfirmationToken(t *testing.T) {
	orgID := uuid.New()
	containerID := uuid.New()
	ticketID := uuid.New()
	phaseID := uuid.New()
	reader := &repairReader{
		views: map[uuid.UUID]*node.NodeView{
			ticketID: {ID: ticketID, OrgID: orgID, NodeType: "ticket", Name: "Ticket", UpdatedAt: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC), Props: map[string]json.RawMessage{"scope_id": mustRaw(t, containerID.String()), "phase": mustRaw(t, "Ready")}},
		},
		listViews: []*node.NodeView{phaseView(t, phaseID, orgID, containerID, "Ready", 1)},
	}
	nodeRepo := &repairNodeRepo{reader: reader}
	console := NewRepairConsole(nodeRepo, reader, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})

	_, err := console.Apply(context.Background(), RepairApplyInput{ActorID: uuid.New(), Class: RepairClassReferenceProperty, NodeID: ticketID, ConfirmationToken: "wrong", Profile: phaseProfile()})
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("Apply err = %v want ErrFailedPrecondition", err)
	}
	if nodeRepo.updatedNode != nil {
		t.Fatal("Apply wrote despite mismatched token")
	}
}

func TestApplyReferencePropertyCleansSourceAndUpdatesTarget(t *testing.T) {
	orgID := uuid.New()
	containerID := uuid.New()
	ticketID := uuid.New()
	phaseID := uuid.New()
	actorID := uuid.New()
	reader := &repairReader{
		views: map[uuid.UUID]*node.NodeView{
			ticketID: {ID: ticketID, OrgID: orgID, NodeType: "ticket", Name: "Ticket", CreatedBy: uuid.New(), UpdatedAt: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC), Props: map[string]json.RawMessage{"scope_id": mustRaw(t, containerID.String()), "phase": mustRaw(t, "Ready")}},
		},
		listViews: []*node.NodeView{phaseView(t, phaseID, orgID, containerID, "Ready", 1)},
	}
	nodeRepo := &repairNodeRepo{reader: reader}
	searcher := &repairSearcher{}
	console := NewRepairConsole(nodeRepo, reader, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, searcher)

	preview, err := console.Preview(context.Background(), RepairPreviewInput{Class: RepairClassReferenceProperty, NodeID: ticketID, Profile: phaseProfile()})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	result, err := console.Apply(context.Background(), RepairApplyInput{ActorID: actorID, Class: RepairClassReferenceProperty, NodeID: ticketID, ConfirmationToken: preview.ConfirmationToken, Profile: phaseProfile()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Applied {
		t.Fatal("Apply Applied = false")
	}
	if _, ok := nodeRepo.updatedNode.Props["phase"]; ok {
		t.Fatal("updated node still contains source field")
	}
	if got := rawUUIDProp(nodeRepo.updatedNode.Props, "phase_id"); got != phaseID {
		t.Fatalf("updated phase_id = %s want %s", got, phaseID)
	}
	if len(nodeRepo.indexedProps) != 1 || nodeRepo.indexedProps[0] != "phase_id" {
		t.Fatalf("indexedProps = %v want [phase_id]", nodeRepo.indexedProps)
	}
	if searcher.indexCount == 0 {
		t.Fatal("Apply did not reindex search document")
	}
}
