package ops

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func TestPreviewReferencePropertyChoosesHighestRank(t *testing.T) {
	orgID := uuid.New()
	containerID := uuid.New()
	ticketID := uuid.New()
	lowPhaseID := uuid.New()
	highPhaseID := uuid.New()
	reader := &repairReader{
		views: map[uuid.UUID]*node.NodeView{
			ticketID: {ID: ticketID, OrgID: orgID, NodeType: "ticket", Name: "Ticket", UpdatedAt: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC), Props: map[string]json.RawMessage{"scope_id": mustRaw(t, containerID.String()), "phase": mustRaw(t, "Done"), "phase_id": mustRaw(t, lowPhaseID.String())}},
		},
		listViews: []*node.NodeView{
			phaseView(t, lowPhaseID, orgID, containerID, "Started", 1),
			phaseView(t, highPhaseID, orgID, containerID, "Done", 3),
		},
	}
	console := NewRepairConsole(&repairNodeRepo{reader: reader}, reader, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})

	preview, err := console.Preview(context.Background(), RepairPreviewInput{Class: RepairClassReferenceProperty, NodeID: ticketID, Profile: phaseProfile()})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !preview.CanApply {
		t.Fatalf("Preview CanApply = false, summary=%q", preview.Summary)
	}
	if preview.ChosenCandidate == nil || preview.ChosenCandidate.NodeID != highPhaseID {
		t.Fatalf("ChosenCandidate = %#v want %s", preview.ChosenCandidate, highPhaseID)
	}
}

func TestMatchReferencePropertyUsesDirectSlugOnlyWhenDeclared(t *testing.T) {
	orgID := uuid.New()
	productID := uuid.New()
	reader := &repairReader{
		views:     map[uuid.UUID]*node.NodeView{},
		listViews: []*node.NodeView{{ID: productID, OrgID: orgID, NodeType: "product", Name: "Product", Props: map[string]json.RawMessage{"slug": mustRaw(t, "alpha")}}},
	}
	matchContext := referenceMatchContext{reader: reader, orgID: orgID, targetType: &node.NodeType{TypeKey: "product", Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "slug"}}}

	view, err := matchContext.resolveReference(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("resolveReference: %v", err)
	}
	if view.ID != productID {
		t.Fatalf("resolved ID = %s want %s", view.ID, productID)
	}
}

func TestMatchReferencePropertyDoesNotUseSlugForScopedProperty(t *testing.T) {
	orgID := uuid.New()
	containerID := uuid.New()
	phaseID := uuid.New()
	reader := &repairReader{
		views:     map[uuid.UUID]*node.NodeView{},
		listViews: []*node.NodeView{{ID: phaseID, OrgID: orgID, NodeType: "phase", Name: "Ready", Props: map[string]json.RawMessage{"parent_id": mustRaw(t, containerID.String()), "slug": mustRaw(t, "ready-slug")}}},
	}
	matchContext := referenceMatchContext{reader: reader, orgID: orgID, scopeID: containerID, targetType: &node.NodeType{TypeKey: "phase", Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedProperty, Property: "name"}}}

	_, err := matchContext.resolveReference(context.Background(), "ready-slug")
	if err == nil {
		t.Fatal("resolveReference succeeded through slug for scoped_property")
	}
}

func TestMatchReferencePropertyUsesScopedSequence(t *testing.T) {
	orgID := uuid.New()
	containerID := uuid.New()
	unitID := uuid.New()
	reader := &repairReader{
		views:     map[uuid.UUID]*node.NodeView{},
		listViews: []*node.NodeView{{ID: unitID, OrgID: orgID, NodeType: "unit", Name: "Unit", Props: map[string]json.RawMessage{"parent_id": mustRaw(t, containerID.String()), "sequence": mustRaw(t, 42)}}},
	}
	matchContext := referenceMatchContext{reader: reader, orgID: orgID, scopeID: containerID, targetType: &node.NodeType{TypeKey: "unit", Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedSequence, Property: "sequence"}}}

	view, err := matchContext.resolveReference(context.Background(), "42")
	if err != nil {
		t.Fatalf("resolveReference: %v", err)
	}
	if view.ID != unitID {
		t.Fatalf("resolved ID = %s want %s", view.ID, unitID)
	}
}
