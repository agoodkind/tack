package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/yosida95/uritemplate/v3"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/project"
	"goodkind.io/tack/internal/domain/workspace"
)

// RegisterResources registers all tack:// URI resources on the server.
func RegisterResources(
	s *mcpserver.MCPServer,
	workspaces workspace.Repository,
	projects project.Repository,
	reader node.NodeReader,
	nodeTypes node.TypeRepository,
	properties node.PropertyRepository,
) {
	s.AddResource(mcpmcp.Resource{
		Name:        "tack-workspaces",
		URI:         "tack://workspaces",
		MIMEType:    "application/json",
		Description: "All workspaces accessible to the authenticated user. Read this first to discover available workspace slugs.",
	}, workspacesHandler(workspaces))

	workspaceTpl := &mcpmcp.URITemplate{
		Template: uritemplate.MustNew("tack://workspace/{slug}"),
	}
	s.AddResourceTemplate(mcpmcp.ResourceTemplate{
		Name:        "tack-workspace",
		URITemplate: workspaceTpl,
		MIMEType:    "application/json",
		Description: "Full workspace description: projects with states, node types, and property definitions. Read this before any workspace-scoped operation.",
	}, workspaceDetailHandler(workspaces, projects, reader, nodeTypes, properties))

	projectTpl := &mcpmcp.URITemplate{
		Template: uritemplate.MustNew("tack://project/{identifier}"),
	}
	s.AddResourceTemplate(mcpmcp.ResourceTemplate{
		Name:        "tack-project",
		URITemplate: projectTpl,
		MIMEType:    "application/json",
		Description: "Project details including workflow states. identifier is the short code e.g. ENG.",
	}, projectDetailHandler(workspaces, projects, reader))

	projectStatesTpl := &mcpmcp.URITemplate{
		Template: uritemplate.MustNew("tack://project/{identifier}/states"),
	}
	s.AddResourceTemplate(mcpmcp.ResourceTemplate{
		Name:        "tack-project-states",
		URITemplate: projectStatesTpl,
		MIMEType:    "application/json",
		Description: "Workflow states for a project, with group names and colors. Use state IDs from this list when creating or updating issues.",
	}, projectStatesHandler(workspaces, projects, reader))
}

// ── workspaces ────────────────────────────────────────────────────────────────

func workspacesHandler(workspaces workspace.Repository) mcpserver.ResourceHandlerFunc {
	return func(ctx context.Context, req mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
		userID, ok := auth.UserID(ctx)
		if !ok {
			return nil, errors.New("not found")
		}
		wss, err := workspaces.ListForUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		body, _ := json.Marshal(map[string]any{"workspaces": wss})
		return []mcpmcp.ResourceContents{
			mcpmcp.TextResourceContents{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(body),
			},
		}, nil
	}
}

// ── workspace/{slug} ──────────────────────────────────────────────────────────

func workspaceDetailHandler(
	workspaces workspace.Repository,
	projects project.Repository,
	reader node.NodeReader,
	nodeTypes node.TypeRepository,
	properties node.PropertyRepository,
) mcpserver.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
		slug := strings.TrimPrefix(req.Params.URI, "tack://workspace/")
		if slug == "" || strings.Contains(slug, "/") {
			return nil, errors.New("not found")
		}
		ws, err := workspaces.GetBySlug(ctx, slug)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil, errors.New("not found")
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
			ss := listStateViewsByProject(ctx, reader, ws.OrgID, ws.ID, p.ID)
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
		return []mcpmcp.ResourceContents{
			mcpmcp.TextResourceContents{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(body),
			},
		}, nil
	}
}

// ── project/{identifier} ──────────────────────────────────────────────────────

func projectDetailHandler(
	workspaces workspace.Repository,
	projects project.Repository,
	reader node.NodeReader,
) mcpserver.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
		identifier := strings.TrimPrefix(req.Params.URI, "tack://project/")
		if identifier == "" || strings.Contains(identifier, "/") {
			return nil, errors.New("not found")
		}
		userID, ok := auth.UserID(ctx)
		if !ok {
			return nil, errors.New("not found")
		}
		wss, _ := workspaces.ListForUser(ctx, userID)
		for _, ws := range wss {
			proj, err := projects.GetByIdentifier(ctx, ws.ID, strings.ToUpper(identifier))
			if err != nil {
				continue
			}
			ss := listStateViewsByProject(ctx, reader, ws.OrgID, ws.ID, proj.ID)
			body, _ := json.Marshal(map[string]any{
				"project":   proj,
				"states":    ss,
				"workspace": map[string]any{"id": ws.ID, "slug": ws.Slug, "name": ws.Name},
			})
			return []mcpmcp.ResourceContents{
				mcpmcp.TextResourceContents{
					URI:      req.Params.URI,
					MIMEType: "application/json",
					Text:     string(body),
				},
			}, nil
		}
		return nil, errors.New("not found")
	}
}

// ── project/{identifier}/states ───────────────────────────────────────────────

func projectStatesHandler(
	workspaces workspace.Repository,
	projects project.Repository,
	reader node.NodeReader,
) mcpserver.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
		path := strings.TrimPrefix(req.Params.URI, "tack://project/")
		identifier := strings.TrimSuffix(path, "/states")
		if identifier == "" || identifier == path || strings.Contains(identifier, "/") {
			return nil, errors.New("not found")
		}
		userID, ok := auth.UserID(ctx)
		if !ok {
			return nil, errors.New("not found")
		}
		wss, _ := workspaces.ListForUser(ctx, userID)
		for _, ws := range wss {
			proj, err := projects.GetByIdentifier(ctx, ws.ID, strings.ToUpper(identifier))
			if err != nil {
				continue
			}
			ss := listStateViewsByProject(ctx, reader, ws.OrgID, ws.ID, proj.ID)
			body, _ := json.Marshal(map[string]any{
				"project_identifier": identifier,
				"states":             ss,
			})
			return []mcpmcp.ResourceContents{
				mcpmcp.TextResourceContents{
					URI:      req.Params.URI,
					MIMEType: "application/json",
					Text:     string(body),
				},
			}, nil
		}
		return nil, errors.New("not found")
	}
}
