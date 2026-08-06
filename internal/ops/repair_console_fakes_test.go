package ops

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	domainsearch "goodkind.io/tack/internal/domain/search"
)

type repairNodeRepo struct {
	reader              *repairReader
	updatedNode         *node.Node
	updatedView         *node.NodeView
	oldProps            map[string]json.RawMessage
	indexedProps        []string
	addRelationships    []*node.Relationship
	removeRelationships []*node.Relationship
}

func (r *repairNodeRepo) Get(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	panic("repairNodeRepo.Get called")
}

func (r *repairNodeRepo) Set(context.Context, *node.Node, *node.NodeView) error {
	panic("repairNodeRepo.Set called")
}

func (r *repairNodeRepo) UpdateAtomic(
	_ context.Context,
	currentNode *node.Node,
	view *node.NodeView,
	oldProps map[string]json.RawMessage,
	indexedProps []string,
	_ []node.ReferenceKey,
	relationshipChanges ...node.RelationshipChanges,
) error {
	r.updatedNode = currentNode
	r.updatedView = view
	r.oldProps = oldProps
	r.indexedProps = indexedProps
	if len(relationshipChanges) > 0 {
		r.addRelationships = relationshipChanges[0].Add
		r.removeRelationships = relationshipChanges[0].Remove
	}
	if r.reader != nil {
		r.reader.views[currentNode.ID] = view
	}
	return nil
}

func (r *repairNodeRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	panic("repairNodeRepo.Delete called")
}

func (r *repairNodeRepo) CreateAtomic(context.Context, *node.Node, *node.NodeView, []*node.Relationship, []string, []node.ReferenceKey, *node.IdempotencyRecord) error {
	panic("repairNodeRepo.CreateAtomic called")
}

func (r *repairNodeRepo) SetReferenceKeys(context.Context, uuid.UUID, uuid.UUID, []node.ReferenceKey) error {
	panic("repairNodeRepo.SetReferenceKeys called")
}

func (r *repairNodeRepo) LookupReference(context.Context, uuid.UUID, string, string) (uuid.UUID, error) {
	panic("repairNodeRepo.LookupReference called")
}

func (r *repairNodeRepo) ListByProperty(context.Context, uuid.UUID, string, string, json.RawMessage) ([]*node.Node, error) {
	panic("repairNodeRepo.ListByProperty called")
}

func (r *repairNodeRepo) AllocateSequence(context.Context, uuid.UUID, uuid.UUID, string) (int64, error) {
	panic("repairNodeRepo.AllocateSequence called")
}

func (r *repairNodeRepo) AllocateSequenceByKey(context.Context, uuid.UUID, string) (int64, error) {
	panic("repairNodeRepo.AllocateSequenceByKey called")
}

func (r *repairNodeRepo) SeedSequenceByKey(context.Context, uuid.UUID, string, int64) error {
	panic("repairNodeRepo.SeedSequenceByKey called")
}

func (r *repairNodeRepo) LookupIdempotencyKey(context.Context, uuid.UUID, string) (*node.IdempotencyRecord, error) {
	panic("repairNodeRepo.LookupIdempotencyKey called")
}

type repairRelationshipRepo struct {
	relationships []*node.Relationship
}

func (r *repairRelationshipRepo) Add(context.Context, *node.Relationship) error {
	panic("repairRelationshipRepo.Add called")
}

func (r *repairRelationshipRepo) Remove(context.Context, uuid.UUID, uuid.UUID, string, uuid.UUID) error {
	panic("repairRelationshipRepo.Remove called")
}

func (r *repairRelationshipRepo) ListBySource(_ context.Context, orgID uuid.UUID, sourceID uuid.UUID, relationType string) ([]*node.Relationship, error) {
	matches := make([]*node.Relationship, 0, len(r.relationships))
	for _, relationship := range r.relationships {
		if relationship.OrgID != orgID || relationship.SourceID != sourceID {
			continue
		}
		if relationType != "" && relationship.RelationType != relationType {
			continue
		}
		matches = append(matches, relationship)
	}
	return matches, nil
}

func (r *repairRelationshipRepo) ListByTarget(context.Context, uuid.UUID, uuid.UUID, string) ([]*node.Relationship, error) {
	panic("repairRelationshipRepo.ListByTarget called")
}

type repairReader struct {
	views     map[uuid.UUID]*node.NodeView
	listViews []*node.NodeView
}

func (r *repairReader) Get(_ context.Context, id uuid.UUID) (*node.NodeView, error) {
	return r.views[id], nil
}

func (r *repairReader) Resolve(_ context.Context, id uuid.UUID) (*node.NodeResolve, error) {
	view := r.views[id]
	if view == nil {
		return nil, nil
	}
	return &node.NodeResolve{OrgID: view.OrgID, NodeType: view.NodeType}, nil
}

func (r *repairReader) List(_ context.Context, query node.NodeListQuery) ([]*node.NodeView, error) {
	views := make([]*node.NodeView, 0, len(r.listViews))
	for _, view := range r.listViews {
		if query.OrgID != uuid.Nil && view.OrgID != query.OrgID {
			continue
		}
		if query.NodeType != "" && view.NodeType != query.NodeType {
			continue
		}
		if query.ByProperty != nil && string(view.Props[query.ByProperty.PropName]) != string(query.ByProperty.Value) {
			continue
		}
		views = append(views, view)
	}
	return views, nil
}

func (r *repairReader) Stream(context.Context, node.NodeListQuery) (<-chan node.NodeStreamResult, error) {
	panic("repairReader.Stream called")
}

type repairTypeRepo struct{ types []*node.NodeType }

func (r *repairTypeRepo) Set(context.Context, *node.NodeType) error {
	panic("repairTypeRepo.Set called")
}

func (r *repairTypeRepo) Get(context.Context, uuid.UUID, uuid.UUID) (*node.NodeType, error) {
	panic("repairTypeRepo.Get called")
}

func (r *repairTypeRepo) List(context.Context, uuid.UUID) ([]*node.NodeType, error) {
	return r.types, nil
}

func (r *repairTypeRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	panic("repairTypeRepo.Delete called")
}

type repairPropRepo struct{ defs []*node.PropertyDef }

func (r *repairPropRepo) Set(context.Context, *node.PropertyDef) error {
	panic("repairPropRepo.Set called")
}

func (r *repairPropRepo) Get(context.Context, uuid.UUID, uuid.UUID) (*node.PropertyDef, error) {
	panic("repairPropRepo.Get called")
}

func (r *repairPropRepo) List(context.Context, uuid.UUID) ([]*node.PropertyDef, error) {
	return r.defs, nil
}

func (r *repairPropRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	panic("repairPropRepo.Delete called")
}

type repairSearcher struct{ indexCount int }

func (s *repairSearcher) Index(context.Context, string, string, *domainsearch.NodeDoc) error {
	s.indexCount++
	return nil
}

func (s *repairSearcher) Delete(context.Context, string, string) error { return nil }

func (s *repairSearcher) Search(context.Context, string, string, map[string]string) ([]domainsearch.NodeDoc, map[string]map[string]int64, error) {
	panic("repairSearcher.Search called")
}

func repairTypes() []*node.NodeType {
	return []*node.NodeType{
		{TypeKey: "container", Slug: "containers", Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "code"}},
		{TypeKey: "phase", Slug: "phases", Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedProperty, Property: "name"}},
		{TypeKey: "ticket", Slug: "tickets", Features: node.Features{"has_phase"}},
	}
}

func repairDefs() []*node.PropertyDef {
	return []*node.PropertyDef{{Name: "parent_id"}, {Name: "scope_id"}, {Name: "phase_id", Type: node.PropertyTypeUUID, Indexed: true, AppliesToFeatures: []string{"has_phase"}, ReferenceTargetTypeKey: "phase"}}
}

func phaseView(t *testing.T, phaseID uuid.UUID, orgID uuid.UUID, containerID uuid.UUID, name string, rank int64) *node.NodeView {
	t.Helper()
	return &node.NodeView{ID: phaseID, OrgID: orgID, NodeType: "phase", Name: name, Props: map[string]json.RawMessage{"parent_id": mustRaw(t, containerID.String()), "rank": mustRaw(t, rank)}}
}

func phaseProfile() *RepairReferenceProfile {
	return &RepairReferenceProfile{TargetProperty: "phase_id", SourceFields: []string{"phase"}, CleanupBehavior: RepairCleanupSourceFields, ConflictPolicy: RepairConflictPreferHighestRank, RankProperty: "rank"}
}

func mustRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}
