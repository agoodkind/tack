package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/project"
	"goodkind.io/tack/internal/domain/state"
	"goodkind.io/tack/internal/domain/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterResources registers all tack:// URI resources on the server.
//
// Static resources:
//
//	tack://workspaces  - all workspaces for the authenticated user
//
// Template resources:
//
//	tack://workspace/{slug}              - workspace description (projects, states, types, props)
//	tack://project/{identifier}          - project details with states
//	tack://project/{identifier}/states   - project workflow states only
func RegisterResources(
	s *mcp.Server,
	workspaces workspace.Repository,
	projects project.Repository,
	states state.Repository,
	nodeTypes node.TypeRepository,
	properties node.PropertyRepository,
) {
	s.AddResource(&mcp.Resource{
		Name:        "tack-workspaces",
		URI:         "tack://workspaces",
		MIMEType:    "application/json",
		Description: "All workspaces accessible to the authenticated user. Read this first to discover available workspace slugs.",
	}, workspacesHandler(workspaces))

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "tack-workspace",
		URITemplate: "tack://workspace/{slug}",
		MIMEType:    "application/json",
		Description: "Full workspace description: projects with states, node types, and property definitions. Read this before any workspace-scoped operation.",
	}, workspaceDetailHandler(workspaces, projects, states, nodeTypes, properties))

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "tack-project",
		URITemplate: "tack://project/{identifier}",
		MIMEType:    "application/json",
		Description: "Project details including workflow states. identifier is the short code e.g. ENG.",
	}, projectDetailHandler(workspaces, projects, states))

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "tack-project-states",
		URITemplate: "tack://project/{identifier}/states",
		MIMEType:    "application/json",
		Description: "Workflow states for a project, with group names and colors. Use state IDs from this list when creating or updating issues.",
	}, projectStatesHandler(workspaces, projects, states))
}

// ── workspaces ────────────────────────────────────────────────────────────────

func workspacesHandler(workspaces workspace.Repository) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		userID, ok := auth.UserID(ctx)
		if !ok {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		wss, err := workspaces.ListForUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		body, _ := json.Marshal(map[string]any{"workspaces": wss})
		return resourceResult(req.Params.URI, body), nil
	}
}

// ── workspace/{slug} ──────────────────────────────────────────────────────────

func workspaceDetailHandler(
	workspaces workspace.Repository,
	projects project.Repository,
	states state.Repository,
	nodeTypes node.TypeRepository,
	properties node.PropertyRepository,
) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		slug := strings.TrimPrefix(req.Params.URI, "tack://workspace/")
		if slug == "" || strings.Contains(slug, "/") {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		ws, err := workspaces.GetBySlug(ctx, slug)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil, mcp.ResourceNotFoundError(req.Params.URI)
			}
			return nil, err
		}
		projs, _ := projects.List(ctx, ws.ID)
		type projectSummary struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Identifier string `json:"identifier"`
			States     any    `json:"states"`
		}
		summaries := make([]projectSummary, 0, len(projs))
		for _, p := range projs {
			ss, _ := states.List(ctx, p.ID)
			summaries = append(summaries, projectSummary{
				ID: p.ID.String(), Name: p.Name, Identifier: p.Identifier, States: ss,
			})
		}
		nts, _ := nodeTypes.List(ctx, ws.OrgID)
		defs, _ := properties.ListDefs(ctx, ws.OrgID, ws.ID, nil)
		body, _ := json.Marshal(map[string]any{
			"workspace":            ws,
			"projects":             summaries,
			"node_types":           nts,
			"property_definitions": defs,
		})
		return resourceResult(req.Params.URI, body), nil
	}
}

// ── project/{identifier} ──────────────────────────────────────────────────────

func projectDetailHandler(
	workspaces workspace.Repository,
	projects project.Repository,
	states state.Repository,
) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		identifier := strings.TrimPrefix(req.Params.URI, "tack://project/")
		if identifier == "" || strings.Contains(identifier, "/") {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		userID, ok := auth.UserID(ctx)
		if !ok {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		wss, _ := workspaces.ListForUser(ctx, userID)
		for _, ws := range wss {
			proj, err := projects.GetByIdentifier(ctx, ws.ID, strings.ToUpper(identifier))
			if err != nil {
				continue
			}
			ss, _ := states.List(ctx, proj.ID)
			body, _ := json.Marshal(map[string]any{
				"project":   proj,
				"states":    ss,
				"workspace": map[string]any{"id": ws.ID, "slug": ws.Slug, "name": ws.Name},
			})
			return resourceResult(req.Params.URI, body), nil
		}
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
}

// ── project/{identifier}/states ───────────────────────────────────────────────

func projectStatesHandler(
	workspaces workspace.Repository,
	projects project.Repository,
	states state.Repository,
) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		path := strings.TrimPrefix(req.Params.URI, "tack://project/")
		identifier := strings.TrimSuffix(path, "/states")
		if identifier == "" || identifier == path || strings.Contains(identifier, "/") {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		userID, ok := auth.UserID(ctx)
		if !ok {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		wss, _ := workspaces.ListForUser(ctx, userID)
		for _, ws := range wss {
			proj, err := projects.GetByIdentifier(ctx, ws.ID, strings.ToUpper(identifier))
			if err != nil {
				continue
			}
			ss, _ := states.List(ctx, proj.ID)
			body, _ := json.Marshal(map[string]any{
				"project_identifier": identifier,
				"states":             ss,
			})
			return resourceResult(req.Params.URI, body), nil
		}
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func resourceResult(uri string, body []byte) *mcp.ReadResourceResult {
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(body),
		}},
	}
}
