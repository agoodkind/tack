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
	reader       *repairReader
	updatedNode  *node.Node
	updatedView  *node.NodeView
	oldProps     map[string]json.RawMessage
	indexedProps []string
}

func (r *repairNodeRepo) Get(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	panic("repairNodeRepo.Get called")
}

func (r *repairNodeRepo) Set(context.Context, *node.Node, *node.NodeView) error {
	panic("repairNodeRepo.Set called")
}

func (r *repairNodeRepo) UpdateAtomic(_ context.Context, currentNode *node.Node, view *node.NodeView, oldProps map[string]json.RawMessage, indexedProps []string) error {
	r.updatedNode = currentNode
	r.updatedView = view
	r.oldProps = oldProps
	r.indexedProps = indexedProps
	if r.reader != nil {
		r.reader.views[currentNode.ID] = view
	}
	return nil
}

func (r *repairNodeRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	panic("repairNodeRepo.Delete called")
}

func (r *repairNodeRepo) CreateAtomic(context.Context, *node.Node, *node.NodeView, []*node.Relationship, []string, *node.IdempotencyRecord) error {
	panic("repairNodeRepo.CreateAtomic called")
}

func (r *repairNodeRepo) ListByProperty(context.Context, uuid.UUID, string, string, json.RawMessage) ([]*node.Node, error) {
	panic("repairNodeRepo.ListByProperty called")
}

func (r *repairNodeRepo) AllocateSequence(context.Context, uuid.UUID, uuid.UUID, string) (int64, error) {
	panic("repairNodeRepo.AllocateSequence called")
}

func (r *repairNodeRepo) GetSlug(context.Context, string, string) (uuid.UUID, error) {
	panic("repairNodeRepo.GetSlug called")
}

func (r *repairNodeRepo) WriteSlug(context.Context, string, string, uuid.UUID) error { return nil }

func (r *repairNodeRepo) DeleteSlug(context.Context, string, string) error { return nil }

func (r *repairNodeRepo) LookupIdempotencyKey(context.Context, uuid.UUID, string) (*node.IdempotencyRecord, error) {
	panic("repairNodeRepo.LookupIdempotencyKey called")
}

type repairReader struct {
	views      map[uuid.UUID]*node.NodeView
	stateViews []*node.NodeView
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
	if query.ByProperty == nil || query.ByProperty.PropName != "parent_id" {
		return nil, nil
	}
	views := make([]*node.NodeView, 0, len(r.stateViews))
	for _, view := range r.stateViews {
		if view.OrgID != query.OrgID || view.NodeType != query.NodeType {
			continue
		}
		if string(view.Props["parent_id"]) != string(query.ByProperty.Value) {
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
		{TypeKey: "project", Slug: "project", Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectSlug, Property: "identifier"}},
		{TypeKey: "state", Slug: "state", Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedProperty, Property: "name"}},
		{TypeKey: "issue", Slug: "issue", Features: node.Features{node.FeatureHasWorkflowStates}},
	}
}

func repairDefs() []*node.PropertyDef {
	return []*node.PropertyDef{
		{Name: "parent_id"},
		{Name: "scope_id"},
		{Name: "state_id", Type: node.PropertyTypeUUID, Indexed: true, AppliesToFeatures: []string{node.FeatureHasWorkflowStates}, ReferenceTargetTypeKey: "state"},
	}
}

func stateView(t *testing.T, stateID uuid.UUID, orgID uuid.UUID, projectID uuid.UUID, name string, sortOrder int64) *node.NodeView {
	t.Helper()
	return &node.NodeView{
		ID:       stateID,
		OrgID:    orgID,
		NodeType: "state",
		Name:     name,
		Props: map[string]json.RawMessage{
			"parent_id":  mustRaw(t, projectID.String()),
			"sort_order": mustRaw(t, sortOrder),
		},
	}
}

func mustRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}
