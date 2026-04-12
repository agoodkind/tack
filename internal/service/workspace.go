package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
)

type WorkspaceService struct {
	entities  node.EntityRepository
	reader    node.NodeReader
	members   org.MemberRepository
	seeder    *WorkspaceSeeder
	searcher  domainsearch.Searcher
	nodeTypes node.TypeRepository
}

func NewWorkspaceService(
	entities node.EntityRepository, reader node.NodeReader, members org.MemberRepository,
	seeder *WorkspaceSeeder, searcher domainsearch.Searcher, nodeTypes node.TypeRepository,
) *WorkspaceService {
	return &WorkspaceService{
		entities: entities, reader: reader, members: members,
		seeder: seeder, searcher: searcher, nodeTypes: nodeTypes,
	}
}

// Create creates a workspace node, seeds defaults, writes slug index, and indexes for search.
func (s *WorkspaceService) Create(ctx context.Context, orgID uuid.UUID, name, slug string) (*node.NodeListView, error) {
	now := time.Now()
	wsID := uuid.New()

	slugBytes, _ := json.Marshal(slug)
	nv := &node.NodeValue{
		ID: wsID, OrgID: orgID, WorkspaceID: uuid.Nil,
		NodeType: node.NodeTypeWorkspace, Name: name, CreatedAt: now, UpdatedAt: now,
	}
	view := &node.NodeListView{
		Version: node.ViewVersion1, ID: wsID, OrgID: orgID, WorkspaceID: uuid.Nil,
		NodeType: node.NodeTypeWorkspace, Name: name, CreatedAt: now, UpdatedAt: now,
		CustomProps: map[string]json.RawMessage{"slug": slugBytes},
	}

	if _, err := s.entities.CreateAtomic(ctx, orgID, wsID, nv, nil, view, nil, nil, uuid.Nil); err != nil {
		return nil, err
	}
	if err := s.entities.WriteSlugIndex(ctx, node.NodeTypeWorkspace, slug, wsID); err != nil {
		return nil, err
	}

	s.seeder.SeedWorkspace(ctx, orgID, wsID)
	_ = s.searcher.Index(ctx, "nodes", wsID.String(), domainsearch.NodeDoc{
		ID: wsID.String(), WorkspaceID: wsID.String(), EntityType: "workspace", Name: name,
	})
	telemetry.L(ctx).Info("workspace.created", slog.String("workspace_id", wsID.String()))
	return view, nil
}

func (s *WorkspaceService) GetByID(ctx context.Context, id uuid.UUID) (*node.NodeListView, error) {
	return s.reader.Get(ctx, id)
}

func (s *WorkspaceService) GetBySlug(ctx context.Context, slug string) (*node.NodeListView, error) {
	id, err := s.entities.GetBySlug(ctx, node.NodeTypeWorkspace, slug)
	if err != nil {
		return nil, err
	}
	return s.reader.Get(ctx, id)
}

func (s *WorkspaceService) List(ctx context.Context, orgID uuid.UUID) ([]*node.NodeListView, error) {
	return s.reader.List(ctx, node.NodeListQuery{
		OrgID: orgID, WorkspaceID: uuid.Nil, NodeType: node.NodeTypeWorkspace,
	})
}

func (s *WorkspaceService) ListForUser(ctx context.Context, userID uuid.UUID) ([]*node.NodeListView, error) {
	orgIDs, err := s.members.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	var all []*node.NodeListView
	for _, orgID := range orgIDs {
		ws, err := s.List(ctx, orgID)
		if err != nil {
			continue
		}
		all = append(all, ws...)
	}
	return all, nil
}

func (s *WorkspaceService) Describe(ctx context.Context, slug string) (*node.NodeListView, []*node.NodeType, error) {
	ws, err := s.GetBySlug(ctx, slug)
	if err != nil {
		return nil, nil, err
	}
	types, err := s.nodeTypes.List(ctx, ws.OrgID)
	if err != nil {
		return nil, nil, err
	}
	return ws, types, nil
}
