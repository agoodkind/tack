package tools

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

type fakeNodeRepo struct {
	scopeChildren map[string][]*node.Node
}

func (r *fakeNodeRepo) Get(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	panic("fakeNodeRepo.Get called")
}

func (r *fakeNodeRepo) Set(context.Context, *node.Node, *node.NodeView) error {
	panic("fakeNodeRepo.Set called")
}

func (r *fakeNodeRepo) UpdateAtomic(
	context.Context,
	*node.Node,
	*node.NodeView,
	map[string]json.RawMessage,
	[]string,
	[]node.ReferenceKey,
	...node.RelationshipChanges,
) error {
	panic("fakeNodeRepo.UpdateAtomic called")
}

func (r *fakeNodeRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	panic("fakeNodeRepo.Delete called")
}

func (r *fakeNodeRepo) CreateAtomic(
	context.Context,
	*node.Node,
	*node.NodeView,
	[]*node.Relationship,
	[]string,
	[]node.ReferenceKey,
	*node.IdempotencyRecord,
) error {
	panic("fakeNodeRepo.CreateAtomic called")
}

func (r *fakeNodeRepo) SetReferenceKeys(
	context.Context,
	uuid.UUID,
	uuid.UUID,
	[]node.ReferenceKey,
) error {
	panic("fakeNodeRepo.SetReferenceKeys called")
}

func (r *fakeNodeRepo) LookupReference(
	context.Context,
	uuid.UUID,
	string,
	string,
) (uuid.UUID, error) {
	panic("fakeNodeRepo.LookupReference called")
}

func (r *fakeNodeRepo) ListByProperty(
	_ context.Context,
	_ uuid.UUID,
	nodeType string,
	propName string,
	value json.RawMessage,
) ([]*node.Node, error) {
	return r.scopeChildren[nodeType+":"+propName+":"+string(value)], nil
}

func (r *fakeNodeRepo) AllocateSequence(
	context.Context,
	uuid.UUID,
	uuid.UUID,
	string,
) (int64, error) {
	panic("fakeNodeRepo.AllocateSequence called")
}

func (r *fakeNodeRepo) AllocateSequenceByKey(context.Context, uuid.UUID, string) (int64, error) {
	panic("fakeNodeRepo.AllocateSequenceByKey called")
}

func (r *fakeNodeRepo) SeedSequenceByKey(context.Context, uuid.UUID, string, int64) error {
	panic("fakeNodeRepo.SeedSequenceByKey called")
}

func (r *fakeNodeRepo) LookupIdempotencyKey(
	context.Context,
	uuid.UUID,
	string,
) (*node.IdempotencyRecord, error) {
	panic("fakeNodeRepo.LookupIdempotencyKey called")
}

type fakePropertyDefs struct {
	defs []*node.PropertyDef
}

func (r *fakePropertyDefs) Set(context.Context, *node.PropertyDef) error {
	panic("fakePropertyDefs.Set called")
}

func (r *fakePropertyDefs) Get(context.Context, uuid.UUID, uuid.UUID) (*node.PropertyDef, error) {
	panic("fakePropertyDefs.Get called")
}

func (r *fakePropertyDefs) List(context.Context, uuid.UUID) ([]*node.PropertyDef, error) {
	return r.defs, nil
}

func (r *fakePropertyDefs) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	panic("fakePropertyDefs.Delete called")
}
