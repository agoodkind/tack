package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/domain/project"
	"goodkind.io/tack/internal/domain/workspace"
	"github.com/google/uuid"
)

// Resolver translates human-readable identifiers to workspace/project pairs.
// The identifier pattern "ENG-42" is split into project identifier "ENG" and
// sequence number 42 by ParseNodeIdentifier; the workspace is always identified
// by its slug.
type Resolver struct {
	workspaces workspace.Repository
	projects   project.Repository
}

// NewResolver creates a Resolver backed by the given repositories.
func NewResolver(workspaces workspace.Repository, projects project.Repository) *Resolver {
	return &Resolver{workspaces: workspaces, projects: projects}
}

// Project resolves workspace_slug + project_identifier to the workspace and project.
func (r *Resolver) Project(ctx context.Context, workspaceSlug, projectIdentifier string) (*workspace.Workspace, *project.Project, error) {
	ws, err := r.workspaces.GetBySlug(ctx, workspaceSlug)
	if err != nil {
		return nil, nil, fmt.Errorf("workspace %q: %w", workspaceSlug, err)
	}
	proj, err := r.projects.GetByIdentifier(ctx, ws.ID, strings.ToUpper(projectIdentifier))
	if err != nil {
		return nil, nil, fmt.Errorf("project %q in workspace %q: %w", projectIdentifier, workspaceSlug, err)
	}
	return ws, proj, nil
}

// Workspace resolves workspace_slug to the workspace.
func (r *Resolver) Workspace(ctx context.Context, slug string) (*workspace.Workspace, error) {
	return r.workspaces.GetBySlug(ctx, slug)
}

// WorkspacesForUser returns all workspaces accessible to the given user.
func (r *Resolver) WorkspacesForUser(ctx context.Context, userID uuid.UUID) ([]*workspace.Workspace, error) {
	return r.workspaces.ListForUser(ctx, userID)
}

// ParseNodeIdentifier splits "ENG-42" into ("ENG", 42).
// Returns an error if the string does not match the PROJECT-N format.
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
