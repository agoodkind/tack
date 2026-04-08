//go:build !fdb

// Package foundationdb provides the FoundationDB extensibility adapter.
// Build with -tags fdb to enable the real implementation.
// Without the tag, all stores are no-ops that compile cleanly on any platform.
package foundationdb

import (
	"context"

	"github.com/agoodkind/tack/internal/domain/node"
	"github.com/google/uuid"
)

// Stores bundles all FDB adapters (noop versions for non-fdb builds).
type Stores struct {
	NodeTypes  *NoopNodeTypeStore
	Properties *NoopPropertyStore
	Activity   *NoopActivityStore
}

// NewStores returns no-op stores. The cluster file is accepted but ignored.
func NewStores(_ string) (*Stores, error) {
	return &Stores{
		NodeTypes:  &NoopNodeTypeStore{},
		Properties: &NoopPropertyStore{},
		Activity:   &NoopActivityStore{},
	}, nil
}

type NoopNodeTypeStore struct{}

func (s *NoopNodeTypeStore) Set(_ context.Context, _ *node.NodeType) error { return nil }
func (s *NoopNodeTypeStore) Get(_ context.Context, _, _ uuid.UUID) (*node.NodeType, error) {
	return nil, nil
}
func (s *NoopNodeTypeStore) List(_ context.Context, _ uuid.UUID) ([]*node.NodeType, error) {
	return nil, nil
}
func (s *NoopNodeTypeStore) Delete(_ context.Context, _, _ uuid.UUID) error { return nil }

type NoopPropertyStore struct{}

func (s *NoopPropertyStore) SetDef(_ context.Context, _ *node.PropertyDef) error { return nil }
func (s *NoopPropertyStore) GetDef(_ context.Context, _, _ uuid.UUID) (*node.PropertyDef, error) {
	return nil, nil
}
func (s *NoopPropertyStore) ListDefs(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ *uuid.UUID) ([]*node.PropertyDef, error) {
	return nil, nil
}
func (s *NoopPropertyStore) DeleteDef(_ context.Context, _ *node.PropertyDef) error { return nil }
func (s *NoopPropertyStore) SetValue(_ context.Context, _, _, _ uuid.UUID, _ any) error {
	return nil
}
func (s *NoopPropertyStore) GetValues(_ context.Context, _, _ uuid.UUID) (node.Properties, error) {
	return node.Properties{}, nil
}
func (s *NoopPropertyStore) DeleteValue(_ context.Context, _, _, _ uuid.UUID) error { return nil }

type NoopActivityStore struct{}

func (s *NoopActivityStore) Append(_ context.Context, _, _ uuid.UUID, _ *node.ActivityEvent) error {
	return nil
}
func (s *NoopActivityStore) List(_ context.Context, _, _, _ uuid.UUID) ([]*node.ActivityEvent, error) {
	return nil, nil
}
