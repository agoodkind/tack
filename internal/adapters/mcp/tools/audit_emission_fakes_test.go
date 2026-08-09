package tools

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	domainsearch "goodkind.io/tack/internal/domain/search"
)

type createAuditNodeRepo struct {
	*fakeNodeRepo
	created *node.Node
}

type referenceAuditNodeRepo struct {
	*fakeNodeRepo
	updated *node.Node
}

func (r *createAuditNodeRepo) CreateAtomic(
	_ context.Context,
	created *node.Node,
	_ *node.NodeView,
	_ []*node.Relationship,
	_ []string,
	_ []node.ReferenceKey,
	_ *node.IdempotencyRecord,
) error {
	r.created = created
	return nil
}

func (r *referenceAuditNodeRepo) UpdateAtomic(
	_ context.Context,
	updated *node.Node,
	_ *node.NodeView,
	_ map[string]json.RawMessage,
	_ []string,
	_ []node.ReferenceKey,
	_ ...node.RelationshipChanges,
) error {
	r.updated = updated
	return nil
}

type createAuditTypes struct {
	types []*node.NodeType
}

func (r *createAuditTypes) Set(context.Context, *node.NodeType) error {
	panic("createAuditTypes.Set called")
}

func (r *createAuditTypes) Get(context.Context, uuid.UUID, uuid.UUID) (*node.NodeType, error) {
	panic("createAuditTypes.Get called")
}

func (r *createAuditTypes) List(context.Context, uuid.UUID) ([]*node.NodeType, error) {
	return r.types, nil
}

func (r *createAuditTypes) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	panic("createAuditTypes.Delete called")
}

type createAuditSearcher struct{}

func (createAuditSearcher) Index(context.Context, string, string, *domainsearch.NodeDoc) error {
	return nil
}

func (createAuditSearcher) Delete(context.Context, string, string) error {
	return nil
}

func (createAuditSearcher) Search(context.Context, string, string, map[string]string) ([]domainsearch.NodeDoc, map[string]map[string]int64, error) {
	panic("createAuditSearcher.Search called")
}
