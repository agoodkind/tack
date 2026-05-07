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

func TestPreviewReferencePropertyRejectsMissingNodeID(t *testing.T) {
	console := NewRepairConsole(&repairNodeRepo{}, &repairReader{}, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})
	_, err := console.Preview(context.Background(), RepairPreviewInput{Class: RepairClassReferenceProperty, Profile: phaseProfile()})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Preview err = %v want ErrInvalidArgument", err)
	}
}

func TestPreviewReferencePropertyReturnsNoopWhenSourceMissing(t *testing.T) {
	ticketID := uuid.New()
	reader := &repairReader{views: map[uuid.UUID]*node.NodeView{
		ticketID: {ID: ticketID, OrgID: uuid.New(), NodeType: "ticket", Name: "Ticket", Props: map[string]json.RawMessage{}, UpdatedAt: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)},
	}}
	console := NewRepairConsole(&repairNodeRepo{reader: reader}, reader, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})

	preview, err := console.Preview(context.Background(), RepairPreviewInput{Class: RepairClassReferenceProperty, NodeID: ticketID, Profile: phaseProfile()})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if preview.NeedsRepair {
		t.Fatal("Preview NeedsRepair = true want false")
	}
	if preview.CanApply {
		t.Fatal("Preview CanApply = true want false")
	}
}

func TestPreviewReferencePropertyBlocksAmbiguousScopedProperty(t *testing.T) {
	orgID := uuid.New()
	containerID := uuid.New()
	ticketID := uuid.New()
	firstPhaseID := uuid.New()
	secondPhaseID := uuid.New()
	reader := &repairReader{
		views: map[uuid.UUID]*node.NodeView{
			ticketID: {ID: ticketID, OrgID: orgID, NodeType: "ticket", Name: "Ticket", Props: map[string]json.RawMessage{"scope_id": mustRaw(t, containerID.String()), "phase": mustRaw(t, "Ready")}},
		},
		listViews: []*node.NodeView{
			phaseView(t, firstPhaseID, orgID, containerID, "Ready", 1),
			phaseView(t, secondPhaseID, orgID, containerID, "Ready", 2),
		},
	}
	console := NewRepairConsole(&repairNodeRepo{reader: reader}, reader, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})

	preview, err := console.Preview(context.Background(), RepairPreviewInput{Class: RepairClassReferenceProperty, NodeID: ticketID, Profile: phaseProfile()})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !preview.NeedsRepair {
		t.Fatal("Preview NeedsRepair = false want true")
	}
	if preview.CanApply {
		t.Fatal("Preview CanApply = true want false")
	}
	if !strings.Contains(preview.Summary, "no reference candidate resolved") {
		t.Fatalf("Preview Summary = %q want unresolved detail", preview.Summary)
	}
}
