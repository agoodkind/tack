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
	"goodkind.io/tack/internal/domain/node"
)

// RegisterResources registers all tack:// URI resources on the server.
func RegisterResources(
	s *mcpserver.MCPServer,
	reader node.NodeReader,
	nodeTypes node.TypeRepository,
	properties node.PropertyRepository,
	r *Resolver,
) {
	s.AddResource(mcpmcp.Resource{
		Name:     "tack-guide",
		URI:      "tack://guide",
		MIMEType: "text/plain",
		Description: "How to use Tack. Read this first.",
	}, guideHandler())

	s.AddResource(mcpmcp.Resource{
		Name:        "tack-workspaces",
		URI:         "tack://workspaces",
		MIMEType:    "application/json",
		Description: "All workspaces accessible to the authenticated user. Read this first to discover available workspace slugs.",
	}, workspacesHandler(r))

	workspaceTpl := &mcpmcp.URITemplate{
		Template: uritemplate.MustNew("tack://workspace/{slug}"),
	}
	s.AddResourceTemplate(mcpmcp.ResourceTemplate{
		Name:        "tack-workspace",
		URITemplate: workspaceTpl,
		MIMEType:    "application/json",
		Description: "Full workspace description: projects with states, node types, and property definitions. Read this before any workspace-scoped operation.",
	}, workspaceDetailHandler(reader, nodeTypes, properties, r))

	projectTpl := &mcpmcp.URITemplate{
		Template: uritemplate.MustNew("tack://project/{identifier}"),
	}
	s.AddResourceTemplate(mcpmcp.ResourceTemplate{
		Name:        "tack-project",
		URITemplate: projectTpl,
		MIMEType:    "application/json",
		Description: "Project details including workflow states. identifier is the short code e.g. ENG.",
	}, projectDetailHandler(reader, r))

	projectStatesTpl := &mcpmcp.URITemplate{
		Template: uritemplate.MustNew("tack://project/{identifier}/states"),
	}
	s.AddResourceTemplate(mcpmcp.ResourceTemplate{
		Name:        "tack-project-states",
		URITemplate: projectStatesTpl,
		MIMEType:    "application/json",
		Description: "Workflow states for a project, with group names and colors. Use state IDs from this list when creating or updating issues.",
	}, projectStatesHandler(reader, r))
}

// ── workspaces ────────────────────────────────────────────────────────────────

func workspacesHandler(r *Resolver) mcpserver.ResourceHandlerFunc {
	return func(ctx context.Context, req mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
		userID, ok := auth.UserID(ctx)
		if !ok {
			return nil, errors.New("not found")
		}
		wss, err := r.WorkspacesForUser(ctx, userID)
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
	reader node.NodeReader,
	nodeTypes node.TypeRepository,
	properties node.PropertyRepository,
	r *Resolver,
) mcpserver.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
		slug := strings.TrimPrefix(req.Params.URI, "tack://workspace/")
		if slug == "" || strings.Contains(slug, "/") {
			return nil, errors.New("not found")
		}
		ws, err := r.Workspace(ctx, slug)
		if err != nil {
			return nil, errors.New("not found")
		}

		projViews, _ := reader.List(ctx, node.NodeListQuery{
			OrgID:       ws.OrgID,
			WorkspaceID: ws.ID,
			NodeType:    node.NodeTypeProject,
		})

		type projectSummary struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Identifier string `json:"identifier"`
			States     any    `json:"states"`
		}
		summaries := make([]projectSummary, 0, len(projViews))
		for _, p := range projViews {
			ss := listStateViewsByProject(ctx, reader, ws.OrgID, ws.ID, p.ID)
			summaries = append(summaries, projectSummary{
				ID: p.ID.String(), Name: p.Name, Identifier: ViewIdentifier(p), States: ss,
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
	reader node.NodeReader,
	r *Resolver,
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
		wss, err := r.WorkspacesForUser(ctx, userID)
		if err != nil {
			return nil, errors.New("not found")
		}
		for _, ws := range wss {
			_, proj, err := r.Project(ctx, ViewSlug(ws), strings.ToUpper(identifier))
			if err != nil {
				continue
			}
			ss := listStateViewsByProject(ctx, reader, ws.OrgID, ws.ID, proj.ID)
			body, _ := json.Marshal(map[string]any{
				"project":   proj,
				"states":    ss,
				"workspace": map[string]any{"id": ws.ID, "slug": ViewSlug(ws), "name": ws.Name},
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
	reader node.NodeReader,
	r *Resolver,
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
		wss, err := r.WorkspacesForUser(ctx, userID)
		if err != nil {
			return nil, errors.New("not found")
		}
		for _, ws := range wss {
			_, proj, err := r.Project(ctx, ViewSlug(ws), strings.ToUpper(identifier))
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

func guideHandler() mcpserver.ResourceHandlerFunc {
	const guide = `Tack is a project management tool. All data is organized as workspaces > projects > issues/epics/cycles/modules.

Getting started:
1. Call tack_list_workspaces to see available workspaces.
2. Call tack_describe_workspace with a workspace_slug to see its projects, workflow states, and available types.
3. Use project identifiers (e.g. TACK, ENG) in tool calls as project_identifier.

Common workflows:
- List issues: tack_list_issues workspace_slug="..." project_identifier="..."
- Create an issue: tack_create_issue workspace_slug="..." project_identifier="..." name="..."
- Search: tack_search workspace_slug="..." query="..."
- My work: tack_my_issues (no parameters)

Every entity type (issue, epic, cycle, module, state, label, and any custom types) supports create, get, list, update, delete. Tool names follow the pattern tack_create_{type}, tack_list_{types}, tack_get_{type}, tack_update_{type}, tack_delete_{type}.

Use workspace_slug and project_identifier (human-readable strings) in all tool calls. Never ask the user for UUIDs.`

	return func(_ context.Context, req mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
		return []mcpmcp.ResourceContents{
			mcpmcp.TextResourceContents{
				URI:      req.Params.URI,
				MIMEType: "text/plain",
				Text:     guide,
			},
		}, nil
	}
}
