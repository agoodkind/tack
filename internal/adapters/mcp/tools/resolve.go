package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
)

// Resolver translates human-readable identifiers to workspace/project context.
// Uses EntityRepository for slug index lookups and NodeReader for entity reads.
// No dedicated workspace or project repositories.
type Resolver struct {
	entities node.EntityRepository
	reader   node.NodeReader
	members  org.MemberRepository
}

// NewResolver creates a Resolver backed by the generic node system.
func NewResolver(entities node.EntityRepository, reader node.NodeReader, members org.MemberRepository) *Resolver {
	return &Resolver{entities: entities, reader: reader, members: members}
}

// Workspace resolves a workspace slug to its NodeListView.
func (r *Resolver) Workspace(ctx context.Context, slug string) (*node.NodeListView, error) {
	wsID, err := r.entities.GetBySlug(ctx, node.NodeTypeWorkspace, slug)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: %w", slug, err)
	}
	if wsID == uuid.Nil {
		return nil, fmt.Errorf("workspace %q: %w", slug, domain.ErrNotFound)
	}
	view, err := r.reader.Get(ctx, wsID)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: %w", slug, err)
	}
	if view == nil {
		return nil, fmt.Errorf("workspace %q: %w", slug, domain.ErrNotFound)
	}
	return view, nil
}

// Project resolves workspace_slug + project_identifier to their NodeListViews.
func (r *Resolver) Project(ctx context.Context, workspaceSlug, projectIdentifier string) (*node.NodeListView, *node.NodeListView, error) {
	ws, err := r.Workspace(ctx, workspaceSlug)
	if err != nil {
		return nil, nil, err
	}

	// Look up project by identifier using the property index.
	identPropID := node.SystemPropID(ws.ID, "identifier")
	nvs, err := r.entities.ListByProperty(ctx, ws.OrgID, ws.ID, node.NodeTypeProject, identPropID, node.TextPropertyValue(strings.ToUpper(projectIdentifier)))
	if err != nil {
		return nil, nil, fmt.Errorf("project %q in workspace %q: %w", projectIdentifier, workspaceSlug, err)
	}
	if len(nvs) == 0 {
		return nil, nil, fmt.Errorf("project %q in workspace %q: %w", projectIdentifier, workspaceSlug, domain.ErrNotFound)
	}

	projView, err := r.reader.Get(ctx, nvs[0].ID)
	if err != nil {
		return nil, nil, fmt.Errorf("project %q: %w", projectIdentifier, err)
	}
	if projView == nil {
		return nil, nil, fmt.Errorf("project %q: %w", projectIdentifier, domain.ErrNotFound)
	}
	return ws, projView, nil
}

// WorkspacesForUser returns all workspace NodeListViews accessible to the given user.
func (r *Resolver) WorkspacesForUser(ctx context.Context, userID uuid.UUID) ([]*node.NodeListView, error) {
	orgIDs, err := r.members.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	var all []*node.NodeListView
	for _, orgID := range orgIDs {
		views, err := r.reader.List(ctx, node.NodeListQuery{
			OrgID:       orgID,
			WorkspaceID: uuid.Nil,
			NodeType:    node.NodeTypeWorkspace,
		})
		if err != nil {
			continue
		}
		all = append(all, views...)
	}
	return all, nil
}

// OrgBySlug resolves an org slug to its NodeListView.
func (r *Resolver) OrgBySlug(ctx context.Context, slug string) (*node.NodeListView, error) {
	orgID, err := r.entities.GetBySlug(ctx, node.NodeTypeOrg, slug)
	if err != nil {
		return nil, fmt.Errorf("org %q: %w", slug, err)
	}
	if orgID == uuid.Nil {
		return nil, fmt.Errorf("org %q: %w", slug, domain.ErrNotFound)
	}
	view, err := r.reader.Get(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("org %q: %w", slug, err)
	}
	if view == nil {
		return nil, fmt.Errorf("org %q: %w", slug, domain.ErrNotFound)
	}
	return view, nil
}

// ViewSlug extracts the slug string from a NodeListView's CustomProps.
func ViewSlug(v *node.NodeListView) string {
	if v == nil || v.CustomProps == nil {
		return ""
	}
	raw, ok := v.CustomProps["slug"]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// ViewIdentifier extracts the identifier string from a NodeListView's CustomProps.
func ViewIdentifier(v *node.NodeListView) string {
	if v == nil || v.CustomProps == nil {
		return ""
	}
	raw, ok := v.CustomProps["identifier"]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// ParseNodeIdentifier splits "ENG-42" into ("ENG", 42).
func ParseNodeIdentifier(identifier string) (projectIdent string, seqID int, err error) {
	idx := strings.LastIndex(identifier, "-")
	if idx <= 0 || idx == len(identifier)-1 {
		return "", 0, fmt.Errorf("invalid identifier %q: expected PROJECT-N format (e.g. ENG-42)", identifier)
	}
	seq, convErr := strconv.Atoi(identifier[idx+1:])
	if convErr != nil || seq <= 0 {
		return "", 0, fmt.Errorf("invalid identifier %q: sequence must be a positive integer", identifier)
	}
	return identifier[:idx], seq, nil
}
