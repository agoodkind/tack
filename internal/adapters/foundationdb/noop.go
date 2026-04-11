//go:build !fdb

// Package foundationdb provides the FoundationDB extensibility adapter.
// Build with -tags fdb to enable the real implementation.
// Without the tag, all stores compile cleanly but perform no operations.
package foundationdb

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/label"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/project"
	"goodkind.io/tack/internal/domain/state"
	"goodkind.io/tack/internal/domain/workspace"
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
	Comments    *NoopCommentStore
	Org         *NoopOrgStore
	Workspace   *NoopWorkspaceStore
	Project     *NoopProjectStore
	State       *NoopStateStore
	Label       *NoopLabelStore
}

func NewStores(_ string, _ *pgxpool.Pool) (*Stores, error) {
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
		Comments:    &NoopCommentStore{},
		Org:         &NoopOrgStore{},
		Workspace:   &NoopWorkspaceStore{},
		Project:     &NoopProjectStore{},
		State:       &NoopStateStore{},
		Label:       &NoopLabelStore{},
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
func (NoopViewStore) Resolve(_ context.Context, _ uuid.UUID) (*node.NodeResolve, error) {
	return nil, domain.ErrNotFound
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

// ── Comments ──────────────────────────────────────────────────────────────────

type NoopCommentStore struct{}

func (s *NoopCommentStore) Create(_ context.Context, _ uuid.UUID, _ *node.Comment) error {
	return nil
}
func (s *NoopCommentStore) List(_ context.Context, _, _ uuid.UUID) ([]*node.Comment, error) {
	return nil, nil
}

// ── Org Repository ───────────────────────────────────────────────────────────────

type NoopOrgStore struct{}

func (s *NoopOrgStore) Create(_ context.Context, _ *org.Org) (*org.Org, error) {
	return nil, nil
}
func (s *NoopOrgStore) GetByID(_ context.Context, _ uuid.UUID) (*org.Org, error) {
	return nil, nil
}
func (s *NoopOrgStore) GetBySlug(_ context.Context, _ string) (*org.Org, error) {
	return nil, nil
}
func (s *NoopOrgStore) AddMember(_ context.Context, _ *org.Member) error {
	return nil
}
func (s *NoopOrgStore) RemoveMember(_ context.Context, _, _ uuid.UUID) error {
	return nil
}
func (s *NoopOrgStore) ListMembers(_ context.Context, _ uuid.UUID) ([]*org.Member, error) {
	return nil, nil
}

// ── Workspace Repository ──────────────────────────────────────────────────────────

type NoopWorkspaceStore struct{}

func (s *NoopWorkspaceStore) Create(_ context.Context, _ *workspace.Workspace) (*workspace.Workspace, error) {
	return nil, nil
}
func (s *NoopWorkspaceStore) GetByID(_ context.Context, _ uuid.UUID) (*workspace.Workspace, error) {
	return nil, nil
}
func (s *NoopWorkspaceStore) GetBySlug(_ context.Context, _ string) (*workspace.Workspace, error) {
	return nil, nil
}
func (s *NoopWorkspaceStore) List(_ context.Context, _ uuid.UUID) ([]*workspace.Workspace, error) {
	return nil, nil
}
func (s *NoopWorkspaceStore) ListForUser(_ context.Context, _ uuid.UUID) ([]*workspace.Workspace, error) {
	return nil, nil
}

// ── Project Repository ────────────────────────────────────────────────────────────

type NoopProjectStore struct{}

func (s *NoopProjectStore) Create(_ context.Context, _ *project.Project) (*project.Project, error) {
	return nil, nil
}
func (s *NoopProjectStore) GetByID(_ context.Context, _, _ uuid.UUID) (*project.Project, error) {
	return nil, nil
}
func (s *NoopProjectStore) GetByIdentifier(_ context.Context, _ uuid.UUID, _ string) (*project.Project, error) {
	return nil, nil
}
func (s *NoopProjectStore) List(_ context.Context, _ uuid.UUID) ([]*project.Project, error) {
	return nil, nil
}
func (s *NoopProjectStore) Update(_ context.Context, _ *project.Project) (*project.Project, error) {
	return nil, nil
}
func (s *NoopProjectStore) Delete(_ context.Context, _ uuid.UUID) error {
	return nil
}
func (s *NoopProjectStore) AllocateSequenceID(_ context.Context, _, _ uuid.UUID, _ string) (int, error) {
	return 0, nil
}

// ── State Repository ──────────────────────────────────────────────────────────────

type NoopStateStore struct{}

func (s *NoopStateStore) Create(_ context.Context, _ *state.State) (*state.State, error) {
	return nil, nil
}
func (s *NoopStateStore) GetByID(_ context.Context, _, _ uuid.UUID) (*state.State, error) {
	return nil, nil
}
func (s *NoopStateStore) List(_ context.Context, _ uuid.UUID) ([]*state.State, error) {
	return nil, nil
}
func (s *NoopStateStore) Update(_ context.Context, _ *state.State) (*state.State, error) {
	return nil, nil
}
func (s *NoopStateStore) Delete(_ context.Context, _ uuid.UUID) error {
	return nil
}

// ── Label Repository ──────────────────────────────────────────────────────────────

type NoopLabelStore struct{}

func (s *NoopLabelStore) Create(_ context.Context, _ *label.Label) (*label.Label, error) {
	return nil, nil
}
func (s *NoopLabelStore) GetByID(_ context.Context, _ uuid.UUID) (*label.Label, error) {
	return nil, nil
}
func (s *NoopLabelStore) List(_ context.Context, _ uuid.UUID, _ *uuid.UUID) ([]*label.Label, error) {
	return nil, nil
}
func (s *NoopLabelStore) Update(_ context.Context, _ *label.Label) (*label.Label, error) {
	return nil, nil
}
func (s *NoopLabelStore) Delete(_ context.Context, _ uuid.UUID) error {
	return nil
}
