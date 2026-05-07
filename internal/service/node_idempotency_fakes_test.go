package service

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	domainsearch "goodkind.io/tack/internal/domain/search"
)

type idempotencyNodeRepo struct {
	records        map[string]*node.IdempotencyRecord
	createRecords  []*node.IdempotencyRecord
	addressWrites  []addressWrite
	addressDeletes []addressDelete
	updatedNodes   []*node.Node
}

type addressWrite struct {
	NodeType    string
	AddressKind node.AddressKind
	Address     string
	NodeID      uuid.UUID
}

type addressDelete struct {
	NodeType    string
	AddressKind node.AddressKind
	Address     string
}

func (r *idempotencyNodeRepo) Get(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	panic("idempotencyNodeRepo.Get called")
}

func (r *idempotencyNodeRepo) Set(context.Context, *node.Node, *node.NodeView) error {
	panic("idempotencyNodeRepo.Set called")
}

func (r *idempotencyNodeRepo) UpdateAtomic(_ context.Context, n *node.Node, _ *node.NodeView, _ map[string]json.RawMessage, _ []string) error {
	r.updatedNodes = append(r.updatedNodes, n)
	return nil
}

func (r *idempotencyNodeRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	panic("idempotencyNodeRepo.Delete called")
}

func (r *idempotencyNodeRepo) CreateAtomic(_ context.Context, n *node.Node, _ *node.NodeView, _ []*node.Relationship, _ []string, record *node.IdempotencyRecord) error {
	if record != nil {
		if r.records == nil {
			r.records = make(map[string]*node.IdempotencyRecord)
		}
		if _, exists := r.records[record.Key]; exists {
			return domain.ErrConflict
		}
		r.records[record.Key] = record
		r.createRecords = append(r.createRecords, record)
	}
	_ = n
	return nil
}

func (r *idempotencyNodeRepo) ListByProperty(context.Context, uuid.UUID, string, string, json.RawMessage) ([]*node.Node, error) {
	panic("idempotencyNodeRepo.ListByProperty called")
}

func (r *idempotencyNodeRepo) AllocateSequence(context.Context, uuid.UUID, uuid.UUID, string) (int64, error) {
	return 1, nil
}

func (r *idempotencyNodeRepo) GetAddress(context.Context, string, node.AddressKind, string) (uuid.UUID, error) {
	panic("idempotencyNodeRepo.GetAddress called")
}

func (r *idempotencyNodeRepo) WriteAddress(_ context.Context, nodeType string, addressKind node.AddressKind, address string, nodeID uuid.UUID) error {
	r.addressWrites = append(r.addressWrites, addressWrite{
		NodeType:    nodeType,
		AddressKind: addressKind,
		Address:     address,
		NodeID:      nodeID,
	})
	return nil
}

func (r *idempotencyNodeRepo) DeleteAddress(_ context.Context, nodeType string, addressKind node.AddressKind, address string) error {
	r.addressDeletes = append(r.addressDeletes, addressDelete{
		NodeType:    nodeType,
		AddressKind: addressKind,
		Address:     address,
	})
	return nil
}

func (r *idempotencyNodeRepo) LookupIdempotencyKey(_ context.Context, _ uuid.UUID, key string) (*node.IdempotencyRecord, error) {
	return r.records[key], nil
}

type idempotencyReader struct {
	orgID uuid.UUID
	views map[uuid.UUID]*node.NodeView
}

func (r *idempotencyReader) Get(_ context.Context, id uuid.UUID) (*node.NodeView, error) {
	return r.views[id], nil
}

func (r *idempotencyReader) Resolve(context.Context, uuid.UUID) (*node.NodeResolve, error) {
	return &node.NodeResolve{OrgID: r.orgID, NodeType: "project"}, nil
}

func (r *idempotencyReader) List(context.Context, node.NodeListQuery) ([]*node.NodeView, error) {
	panic("idempotencyReader.List called")
}

func (r *idempotencyReader) Stream(context.Context, node.NodeListQuery) (<-chan node.NodeStreamResult, error) {
	panic("idempotencyReader.Stream called")
}

type idempotencyTypes struct {
	types []*node.NodeType
}

func (r *idempotencyTypes) Set(context.Context, *node.NodeType) error {
	panic("idempotencyTypes.Set called")
}

func (r *idempotencyTypes) Get(context.Context, uuid.UUID, uuid.UUID) (*node.NodeType, error) {
	panic("idempotencyTypes.Get called")
}

func (r *idempotencyTypes) List(context.Context, uuid.UUID) ([]*node.NodeType, error) {
	return r.types, nil
}

func (r *idempotencyTypes) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	panic("idempotencyTypes.Delete called")
}

type idempotencyProps struct {
	defs []*node.PropertyDef
}

func (r *idempotencyProps) Set(context.Context, *node.PropertyDef) error {
	panic("idempotencyProps.Set called")
}

func (r *idempotencyProps) Get(context.Context, uuid.UUID, uuid.UUID) (*node.PropertyDef, error) {
	panic("idempotencyProps.Get called")
}

func (r *idempotencyProps) List(context.Context, uuid.UUID) ([]*node.PropertyDef, error) {
	return r.defs, nil
}

func (r *idempotencyProps) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	panic("idempotencyProps.Delete called")
}

type idempotencySearcher struct{}

func (s idempotencySearcher) Index(context.Context, string, string, *domainsearch.NodeDoc) error {
	return nil
}

func (s idempotencySearcher) Delete(context.Context, string, string) error {
	return nil
}

func (s idempotencySearcher) Search(context.Context, string, string, map[string]string) ([]domainsearch.NodeDoc, map[string]map[string]int64, error) {
	panic("idempotencySearcher.Search called")
}
