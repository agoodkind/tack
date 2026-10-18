package datagen

import (
	"context"
	"encoding/json"
	"maps"
	"strconv"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// repairFixtureIdentifier is the identifier of the repair fixture project.
const repairFixtureIdentifier = "QAREPAIR"

// repairFixtureBuilder creates fixture records in one organization as one
// actor. Its err field stores the first error, and every method does nothing
// after an error. Legacy writes use NodeStore.UpdateAtomic,
// RelationshipStore.Add, and RelationshipStore.Remove directly because the
// checked mutation path rejects the invalid values that the repair corrects.
type repairFixtureBuilder struct {
	nodes   *service.NodeService
	stores  *fdbadapter.Stores
	orgID   uuid.UUID
	actorID uuid.UUID
	err     error
}

// build creates the workspace, the project with identifier QAREPAIR, the
// epic, and every repair record in order, then returns their IDs.
func (b *repairFixtureBuilder) build(ctx context.Context) RepairFixture {
	var fixture RepairFixture
	workspaceID := b.create(ctx, b.orgID, b.orgID, "workspace", "QA repair workspace", map[string]json.RawMessage{"slug": repairFixtureText("repair")})
	fixture.ProjectID = b.create(ctx, workspaceID, workspaceID, "project", "QA repair project", map[string]json.RawMessage{
		"slug": repairFixtureText("repair"), "identifier": repairFixtureText(repairFixtureIdentifier),
	})
	project := fixture.ProjectID
	fixture.EpicID = b.create(ctx, project, project, "epic", "Grouping epic", nil)
	fixture.MultipleEdges = b.create(ctx, project, project, "issue", "Issue with two parent edges", nil)
	b.addChildOf(ctx, fixture.MultipleEdges, fixture.EpicID)
	fixture.LegacyStatus = b.create(ctx, project, project, "issue", "Issue with legacy values", nil)
	b.setLegacyProps(ctx, fixture.LegacyStatus, map[string]json.RawMessage{"status": repairFixtureText("Todo"), "priority": repairFixtureText("High")})
	fixture.PrintedParent = b.create(ctx, fixture.EpicID, project, "issue", "Issue with a printed parent", nil)
	b.setLegacyProps(ctx, fixture.PrintedParent, map[string]json.RawMessage{"parent_id": repairFixtureText(repairFixtureIdentifier + "-1")})
	fixture.AbsentParent = b.create(ctx, project, project, "issue", "Issue with an absent parent", nil)
	b.setLegacyProps(ctx, fixture.AbsentParent, map[string]json.RawMessage{"parent_id": repairFixtureText(uuid.New().String())})
	original := b.create(ctx, project, project, "issue", "Original parent", nil)
	fixture.NoticeTarget = b.create(ctx, project, project, "issue", "Duplicate target", nil)
	fixture.Notice = b.create(ctx, original, project, "comment", "Duplicate of the target issue",
		map[string]json.RawMessage{"description": repairFixtureText("Duplicate of the target issue")})
	b.removeChildOf(ctx, fixture.Notice, original)
	b.setLegacyProps(ctx, fixture.Notice, map[string]json.RawMessage{"parent_id": repairFixtureText(uuid.New().String())})
	fixture.SmokeComment = b.create(ctx, original, project, "comment", "Smoke comment",
		map[string]json.RawMessage{"description": repairFixtureText("Smoke comment")})
	fixture.DoneState = b.state(ctx, project, "Done")
	return fixture
}

// create creates one node of typeKey under parentID with scopeID through
// NodeService.Create and returns its ID.
func (b *repairFixtureBuilder) create(ctx context.Context, parentID, scopeID uuid.UUID, typeKey, name string, props map[string]json.RawMessage) uuid.UUID {
	if b.err != nil {
		return uuid.Nil
	}
	created, err := b.nodes.Create(ctx, service.CreateInput{
		ParentID: parentID, ScopeID: scopeID, NodeTypeKey: typeKey, Name: name, Props: props,
		Relationships: nil, ActorID: b.actorID, IdempotencyKey: "", IdempotencyFingerprint: "", IdempotencySource: "",
	})
	if err != nil {
		b.err = loggedError(ctx, "qa datagen: create repair fixture "+typeKey+" "+name, err)
		return uuid.Nil
	}
	return created.View.ID
}

// addChildOf adds a child_of edge from sourceID to targetID through the
// relationship store without changing parent_id.
func (b *repairFixtureBuilder) addChildOf(ctx context.Context, sourceID, targetID uuid.UUID) {
	if b.err != nil {
		return
	}
	relationship := &node.Relationship{
		OrgID: b.orgID, SourceID: sourceID, RelationType: node.RelChildOf, TargetID: targetID,
		CreatedBy: b.actorID, CreatedAt: clock.Now().UTC(), Props: nil,
	}
	if err := b.stores.Relationships.Add(ctx, relationship); err != nil {
		b.err = loggedError(ctx, "qa datagen: add the legacy child_of edge from "+sourceID.String(), err)
	}
}

// removeChildOf removes the child_of edge from sourceID to targetID through
// the relationship store without changing parent_id.
func (b *repairFixtureBuilder) removeChildOf(ctx context.Context, sourceID, targetID uuid.UUID) {
	if b.err != nil {
		return
	}
	if err := b.stores.Relationships.Remove(ctx, b.orgID, sourceID, node.RelChildOf, targetID); err != nil {
		b.err = loggedError(ctx, "qa datagen: remove the child_of edge from "+sourceID.String(), err)
	}
}

// setLegacyProps reads the stored primary record and view for nodeID.
// It sets each property in props on both records and preserves every other
// stored property. It writes both records through NodeStore.UpdateAtomic
// without validation.
func (b *repairFixtureBuilder) setLegacyProps(ctx context.Context, nodeID uuid.UUID, props map[string]json.RawMessage) {
	if b.err != nil {
		return
	}
	current, err := b.stores.Nodes.Get(ctx, b.orgID, nodeID)
	if err != nil || current == nil {
		b.err = loggedError(ctx, "qa datagen: read repair fixture node "+nodeID.String(), errMissing(err))
		return
	}
	view, err := b.stores.Views.Get(ctx, nodeID)
	if err != nil || view == nil {
		b.err = loggedError(ctx, "qa datagen: read repair fixture view "+nodeID.String(), errMissing(err))
		return
	}
	previous := maps.Clone(current.Props)
	maps.Copy(current.Props, props)
	view.Props = maps.Clone(current.Props)
	if err := b.stores.Nodes.UpdateAtomic(ctx, current, view, previous, nil, nil); err != nil {
		b.err = loggedError(ctx, "qa datagen: write legacy properties of "+nodeID.String(), err)
	}
}

// state returns the ID of the state node named name among the hierarchy
// children of projectID.
func (b *repairFixtureBuilder) state(ctx context.Context, projectID uuid.UUID, name string) uuid.UUID {
	if b.err != nil {
		return uuid.Nil
	}
	children, err := b.stores.Relationships.ListByTarget(ctx, b.orgID, projectID, node.RelChildOf)
	if err != nil {
		b.err = loggedError(ctx, "qa datagen: list the children of project "+projectID.String(), err)
		return uuid.Nil
	}
	for _, child := range children {
		view, err := b.stores.Views.Get(ctx, child.SourceID)
		if err != nil {
			b.err = loggedError(ctx, "qa datagen: read project child "+child.SourceID.String(), err)
			return uuid.Nil
		}
		if view != nil && view.NodeType == "state" && view.Name == name {
			return view.ID
		}
	}
	b.err = loggedError(ctx, "qa datagen: find the "+name+" state of project "+projectID.String(), errMissing(nil))
	return uuid.Nil
}

// repairFixtureText returns text as a JSON string.
func repairFixtureText(text string) json.RawMessage {
	return json.RawMessage(strconv.Quote(text))
}
