//go:build !fdb

// Package foundationdb provides the FoundationDB extensibility adapter.
// Build with -tags fdb to enable the real implementation.
// Without the tag, all stores compile cleanly but perform no operations.
package foundationdb

import (
	"context"

	"goodkind.io/tack/internal/domain/node"
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
	NodeDeleter *NoopNodeDeleter
	Entities    *NoopEntityStore
	Automations *NoopAutomationStore
	Views       *NoopViewStore
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
		NodeDeleter: &NoopNodeDeleter{},
		Entities:    &NoopEntityStore{},
		Automations: &NoopAutomationStore{},
		Views:       &NoopViewStore{},
	}, nil
}

// ── NodeDeleter ───────────────────────────────────────────────────────────────

type NoopNodeDeleter struct{}

func (s *NoopNodeDeleter) DeleteNode(_ context.Context, _, _ uuid.UUID) error { return nil }

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

// ── Entities ──────────────────────────────────────────────────────────────────

type NoopEntityStore struct{}

func (s *NoopEntityStore) Set(_ context.Context, _ *node.NodeValue, _ map[uuid.UUID]*node.PropertyValue, _ *node.NodeListView) error {
	return nil
}
func (s *NoopEntityStore) Get(_ context.Context, _, _ uuid.UUID, _ string, _ uuid.UUID) (*node.NodeValue, error) {
	return nil, nil
}
func (s *NoopEntityStore) Delete(_ context.Context, _ *node.NodeValue) error { return nil }
func (s *NoopEntityStore) ListByProject(_ context.Context, _, _ uuid.UUID, _ string) ([]*node.NodeValue, error) {
	return nil, nil
}
func (s *NoopEntityStore) ListByState(_ context.Context, _, _ uuid.UUID, _ string, _ uuid.UUID) ([]*node.NodeValue, error) {
	return nil, nil
}
func (s *NoopEntityStore) ListByProperty(_ context.Context, _, _ uuid.UUID, _ string, _ uuid.UUID, _ *node.PropertyValue) ([]*node.NodeValue, error) {
	return nil, nil
}
func (s *NoopEntityStore) AllocateSequenceID(_ context.Context, _, _ uuid.UUID, _ string) (int64, error) {
	return 1, nil
}
func (s *NoopEntityStore) CreateAtomic(_ context.Context, _, _ uuid.UUID, _ *node.NodeValue, _ map[uuid.UUID]*node.PropertyValue, _ *node.NodeListView, _ []uuid.UUID, _ []uuid.UUID, _ uuid.UUID) (int64, error) {
	return 1, nil
}
func (s *NoopEntityStore) GetBySequence(_ context.Context, _, _ uuid.UUID, _ string, _ int64) (uuid.UUID, error) {
	return uuid.Nil, nil
}

// ── Views (NodeReader) ────────────────────────────────────────────────────────

type NoopViewStore struct{}

func (s *NoopViewStore) Get(_ context.Context, _ uuid.UUID) (*node.NodeListView, error) {
	return nil, nil
}
func (s *NoopViewStore) List(_ context.Context, _ node.NodeListQuery) ([]*node.NodeListView, error) {
	return nil, nil
}
func (s *NoopViewStore) Stream(_ context.Context, _ node.NodeListQuery) (<-chan node.NodeStreamResult, error) {
	ch := make(chan node.NodeStreamResult)
	close(ch)
	return ch, nil
}

// ── Automations ───────────────────────────────────────────────────────────────

type NoopAutomationStore struct{}

func (s *NoopAutomationStore) Set(_ context.Context, _ *node.AutomationRule) error { return nil }
func (s *NoopAutomationStore) Get(_ context.Context, _, _ uuid.UUID) (*node.AutomationRule, error) {
	return nil, nil
}
func (s *NoopAutomationStore) ListByTrigger(_ context.Context, _, _ uuid.UUID, _ string, _ node.AutomationTrigger) ([]*node.AutomationRule, error) {
	return nil, nil
}
func (s *NoopAutomationStore) Delete(_ context.Context, _, _ uuid.UUID) error { return nil }

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
func (s *NoopContainmentStore) IssuesInEpic(_ context.Context, _, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (s *NoopContainmentStore) EpicsContainingIssue(_ context.Context, _, _ uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}
