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

func TestApplyStrayAliasStateRejectsMissingActorID(t *testing.T) {
	console := NewRepairConsole(&repairNodeRepo{}, &repairReader{}, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})
	_, err := console.Apply(context.Background(), RepairApplyInput{Class: RepairClassStrayAliasState, NodeID: uuid.New(), ConfirmationToken: "token"})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Apply err = %v want ErrInvalidArgument", err)
	}
}

func TestApplyStrayAliasStateRejectsBlankConfirmationToken(t *testing.T) {
	console := NewRepairConsole(&repairNodeRepo{}, &repairReader{}, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})
	_, err := console.Apply(context.Background(), RepairApplyInput{ActorID: uuid.New(), Class: RepairClassStrayAliasState, NodeID: uuid.New(), ConfirmationToken: "   "})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Apply err = %v want ErrInvalidArgument", err)
	}
}

func TestApplyStrayAliasStateRejectsStalePreviewAfterNodeChange(t *testing.T) {
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
	reader.views[issueID].UpdatedAt = reader.views[issueID].UpdatedAt.Add(time.Second)

	_, err = console.Apply(context.Background(), RepairApplyInput{ActorID: uuid.New(), Class: RepairClassStrayAliasState, NodeID: issueID, ConfirmationToken: preview.ConfirmationToken})
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("Apply err = %v want ErrFailedPrecondition", err)
	}
	if nodeRepo.updatedNode != nil {
		t.Fatal("Apply wrote despite stale preview token")
	}
}

func TestApplyStrayAliasStateRecordsActorAndIndexedProps(t *testing.T) {
	orgID := uuid.New()
	projectID := uuid.New()
	issueID := uuid.New()
	stateID := uuid.New()
	actorID := uuid.New()
	reader := &repairReader{
		views: map[uuid.UUID]*node.NodeView{
			issueID: {
				ID: issueID, OrgID: orgID, NodeType: "issue", Name: "Issue",
				CreatedBy: uuid.New(), UpdatedAt: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
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
	_, err = console.Apply(context.Background(), RepairApplyInput{ActorID: actorID, Class: RepairClassStrayAliasState, NodeID: issueID, ConfirmationToken: preview.ConfirmationToken})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if nodeRepo.updatedNode == nil {
		t.Fatal("Apply updatedNode = nil")
	}
	if nodeRepo.updatedNode.UpdatedBy != actorID {
		t.Fatalf("updated node UpdatedBy = %s want %s", nodeRepo.updatedNode.UpdatedBy, actorID)
	}
	if nodeRepo.updatedView == nil {
		t.Fatal("Apply updatedView = nil")
	}
	if nodeRepo.updatedView.UpdatedBy != actorID {
		t.Fatalf("updated view UpdatedBy = %s want %s", nodeRepo.updatedView.UpdatedBy, actorID)
	}
	if rawUUIDProp(nodeRepo.oldProps, "state_id") != uuid.Nil {
		t.Fatalf("oldProps state_id = %s want nil", rawUUIDProp(nodeRepo.oldProps, "state_id"))
	}
	if len(nodeRepo.indexedProps) != 1 || nodeRepo.indexedProps[0] != "state_id" {
		t.Fatalf("indexedProps = %v want [state_id]", nodeRepo.indexedProps)
	}
}
