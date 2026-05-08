package ops

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func TestApplyParentReferenceUpdatesParentAndRelationship(t *testing.T) {
	orgID := uuid.New()
	projectID := uuid.New()
	issueID := uuid.New()
	epicID := uuid.New()
	actorID := uuid.New()
	reader := &repairReader{
		views: map[uuid.UUID]*node.NodeView{
			issueID: {
				ID:        issueID,
				OrgID:     orgID,
				NodeType:  "ticket",
				Name:      "Ticket",
				CreatedBy: uuid.New(),
				UpdatedAt: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
				Props: map[string]json.RawMessage{
					"scope_id":          mustRaw(t, projectID.String()),
					"parent_epic_title": mustRaw(t, "Migration Epic"),
				},
			},
		},
		listViews: []*node.NodeView{
			{
				ID:       epicID,
				OrgID:    orgID,
				NodeType: "epic",
				Name:     "Migration Epic",
				Props: map[string]json.RawMessage{
					"parent_id": mustRaw(t, projectID.String()),
					"title":     mustRaw(t, "Migration Epic"),
				},
			},
		},
	}
	nodeRepo := &repairNodeRepo{reader: reader}
	console := NewRepairConsole(nodeRepo, reader, &repairTypeRepo{types: repairParentTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})
	profile := &RepairReferenceProfile{
		Name:                "parent-epic",
		TargetTypeKey:       "epic",
		TargetMatchProperty: "title",
		SourceFields:        []string{"parent_epic_title"},
		ScopeFields:         []string{"scope_id"},
	}

	preview, err := console.Preview(context.Background(), RepairPreviewInput{Class: RepairClassParentReference, NodeID: issueID, Profile: profile})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !preview.CanApply {
		t.Fatal("Preview CanApply = false want true")
	}
	if got := rawUUIDProp(preview.PlannedProps, "parent_id"); got != epicID {
		t.Fatalf("planned parent_id = %s want %s", got, epicID)
	}
	if len(preview.PlannedRelationships) != 1 || preview.PlannedRelationships[0].Action != RepairRelationshipAdd {
		t.Fatalf("planned relationships = %#v want one add", preview.PlannedRelationships)
	}

	result, err := console.Apply(context.Background(), RepairApplyInput{ActorID: actorID, Class: RepairClassParentReference, NodeID: issueID, ConfirmationToken: preview.ConfirmationToken, Profile: profile})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Applied {
		t.Fatal("Apply Applied = false")
	}
	if got := rawUUIDProp(nodeRepo.updatedNode.Props, "parent_id"); got != epicID {
		t.Fatalf("updated parent_id = %s want %s", got, epicID)
	}
	if _, ok := nodeRepo.updatedNode.Props["parent_epic_title"]; ok {
		t.Fatal("updated node still contains parent_epic_title")
	}
	if len(nodeRepo.addRelationships) != 1 {
		t.Fatalf("addRelationships len = %d want 1", len(nodeRepo.addRelationships))
	}
	added := nodeRepo.addRelationships[0]
	if added.SourceID != issueID || added.TargetID != epicID || added.RelationType != node.RelChildOf {
		t.Fatalf("added relationship = %#v", added)
	}
	if added.CreatedBy != actorID {
		t.Fatalf("added CreatedBy = %s want %s", added.CreatedBy, actorID)
	}
	if added.CreatedAt.IsZero() {
		t.Fatal("added CreatedAt is zero")
	}
}

func TestPreviewParentReferenceAddsMissingRelationshipWhenParentPropExists(t *testing.T) {
	orgID := uuid.New()
	projectID := uuid.New()
	issueID := uuid.New()
	epicID := uuid.New()
	reader := &repairReader{
		views: map[uuid.UUID]*node.NodeView{
			issueID: {
				ID:        issueID,
				OrgID:     orgID,
				NodeType:  "ticket",
				Name:      "Ticket",
				CreatedBy: uuid.New(),
				UpdatedAt: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
				Props: map[string]json.RawMessage{
					"scope_id":          mustRaw(t, projectID.String()),
					"parent_id":         mustRaw(t, epicID.String()),
					"parent_epic_title": mustRaw(t, "Migration Epic"),
				},
			},
		},
		listViews: []*node.NodeView{
			{
				ID:       epicID,
				OrgID:    orgID,
				NodeType: "epic",
				Name:     "Migration Epic",
				Props: map[string]json.RawMessage{
					"parent_id": mustRaw(t, projectID.String()),
					"title":     mustRaw(t, "Migration Epic"),
				},
			},
		},
	}
	console := NewRepairConsole(
		&repairNodeRepo{reader: reader},
		reader,
		&repairTypeRepo{types: repairParentTypes()},
		&repairPropRepo{defs: repairDefs()},
		&repairSearcher{},
		&repairRelationshipRepo{},
	)
	profile := &RepairReferenceProfile{
		Name:                "parent-epic",
		TargetTypeKey:       "epic",
		TargetMatchProperty: "title",
		SourceFields:        []string{"parent_epic_title"},
		ScopeFields:         []string{"scope_id"},
	}

	preview, err := console.Preview(context.Background(), RepairPreviewInput{Class: RepairClassParentReference, NodeID: issueID, Profile: profile})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if _, ok := preview.PlannedProps["parent_id"]; ok {
		t.Fatal("preview planned parent_id update despite existing parent_id")
	}
	if string(preview.PlannedProps["parent_epic_title"]) != "null" {
		t.Fatalf("planned parent_epic_title = %s want null", preview.PlannedProps["parent_epic_title"])
	}
	if len(preview.PlannedRelationships) != 1 || preview.PlannedRelationships[0].TargetID != epicID {
		t.Fatalf("planned relationships = %#v want one add to epic", preview.PlannedRelationships)
	}
}

func repairParentTypes() []*node.NodeType {
	types := repairTypes()
	types = append(types, &node.NodeType{TypeKey: "epic", Slug: "epics", Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedProperty, Property: "name"}})
	return types
}
