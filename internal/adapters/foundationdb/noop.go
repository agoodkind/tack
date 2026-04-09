//go:build !fdb

// Package foundationdb provides the FoundationDB extensibility adapter.
// Build with -tags fdb to enable the real implementation.
// Without the tag, all stores compile cleanly but perform no operations.
package foundationdb

import (
	"context"

	"github.com/agoodkind/tack/internal/domain/node"
	"github.com/google/uuid"
)

type Stores struct {
	NodeTypes   *NoopNodeTypeStore
	Properties  *NoopPropertyStore
	Activity    *NoopActivityStore
	Assignments *NoopAssignmentStore
	Labels      *NoopNodeLabelStore
	Membership  *NoopMembershipStore
	Containment *NoopContainmentStore
}

func NewStores(_ string) (*Stores, error) {
	return &Stores{
		NodeTypes:   &NoopNodeTypeStore{},
		Properties:  &NoopPropertyStore{},
		Activity:    &NoopActivityStore{},
		Assignments: &NoopAssignmentStore{},
		Labels:      &NoopNodeLabelStore{},
		Membership:  &NoopMembershipStore{},
		Containment: &NoopContainmentStore{},
	}, nil
}

// ── NodeTypes ─────────────────────────────────────────────────────────────────

type NoopNodeTypeStore struct{}

func (s *NoopNodeTypeStore) Set(_ context.Context, _ *node.NodeType) error { return nil }
func (s *NoopNodeTypeStore) Get(_ context.Context, _, _ uuid.UUID) (*node.NodeType, error) {
	return nil, nil
}
func (s *NoopNodeTypeStore) List(_ context.Context, _ uuid.UUID) ([]*node.NodeType, error) {
	return nil, nil
}
func (s *NoopNodeTypeStore) Delete(_ context.Context, _, _ uuid.UUID) error { return nil }

// ── Properties ────────────────────────────────────────────────────────────────

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

// ── Activity ──────────────────────────────────────────────────────────────────

type NoopActivityStore struct{}

func (s *NoopActivityStore) Append(_ context.Context, _, _ uuid.UUID, _ *node.ActivityEvent) error {
	return nil
}
func (s *NoopActivityStore) List(_ context.Context, _, _, _ uuid.UUID) ([]*node.ActivityEvent, error) {
	return nil, nil
}

// ── Assignments ───────────────────────────────────────────────────────────────

type NoopAssignmentStore struct{}

func (s *NoopAssignmentStore) SetAll(_ context.Context, _, _ uuid.UUID, _ []uuid.UUID, _ uuid.UUID) error {
	return nil
}
func (s *NoopAssignmentStore) ListByNode(_ context.Context, _, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (s *NoopAssignmentStore) ListByUser(_ context.Context, _, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

// ── Labels ────────────────────────────────────────────────────────────────────

type NoopNodeLabelStore struct{}

func (s *NoopNodeLabelStore) SetAll(_ context.Context, _, _ uuid.UUID, _ []uuid.UUID, _ uuid.UUID) error {
	return nil
}
func (s *NoopNodeLabelStore) ListByNode(_ context.Context, _, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (s *NoopNodeLabelStore) ListByLabel(_ context.Context, _, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

// ── Membership ────────────────────────────────────────────────────────────────

type EntityMembership struct {
	EntityType string
	EntityID   uuid.UUID
	Role       string
}

type UserMembership struct {
	UserID uuid.UUID
	Role   string
}

type NoopMembershipStore struct{}

func (s *NoopMembershipStore) Set(_ context.Context, _, _ uuid.UUID, _ string, _ uuid.UUID, _ string, _ uuid.UUID) error {
	return nil
}
func (s *NoopMembershipStore) Delete(_ context.Context, _, _ uuid.UUID, _ string, _ uuid.UUID) error {
	return nil
}
func (s *NoopMembershipStore) Get(_ context.Context, _, _ uuid.UUID, _ string, _ uuid.UUID) (interface{}, error) {
	return nil, nil
}
func (s *NoopMembershipStore) ListByUser(_ context.Context, _, _ uuid.UUID) ([]EntityMembership, error) {
	return nil, nil
}
func (s *NoopMembershipStore) ListByEntity(_ context.Context, _ uuid.UUID, _ string, _ uuid.UUID) ([]UserMembership, error) {
	return nil, nil
}
func (s *NoopMembershipStore) ListByRole(_ context.Context, _ uuid.UUID, _ string, _ uuid.UUID, _ string) ([]uuid.UUID, error) {
	return nil, nil
}

// ── Containment ───────────────────────────────────────────────────────────────

type NoopContainmentStore struct{}

func (s *NoopContainmentStore) AddIssueToModule(_ context.Context, _, _, _, _ uuid.UUID) error {
	return nil
}
func (s *NoopContainmentStore) RemoveIssueFromModule(_ context.Context, _, _, _ uuid.UUID) error {
	return nil
}
func (s *NoopContainmentStore) IssuesInModule(_ context.Context, _, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (s *NoopContainmentStore) ModulesContainingIssue(_ context.Context, _, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (s *NoopContainmentStore) AddIssueToCycle(_ context.Context, _, _, _, _ uuid.UUID) error {
	return nil
}
func (s *NoopContainmentStore) RemoveIssueFromCycle(_ context.Context, _, _, _ uuid.UUID) error {
	return nil
}
func (s *NoopContainmentStore) IssuesInCycle(_ context.Context, _, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (s *NoopContainmentStore) CyclesContainingIssue(_ context.Context, _, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
