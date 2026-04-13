package tools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/yosida95/uritemplate/v3"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// RegisterResources registers all tack:// URI resources on the server.
func RegisterResources(
	s *mcpserver.MCPServer,
	reader node.NodeReader,
	r *Resolver,
	allTypes []*node.NodeType,
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
	}, workspaceDetailHandler(reader, r, allTypes))

	projectTpl := &mcpmcp.URITemplate{
		Template: uritemplate.MustNew("tack://project/{identifier}"),
	}
	s.AddResourceTemplate(mcpmcp.ResourceTemplate{
		Name:        "tack-project",
		URITemplate: projectTpl,
		MIMEType:    "application/json",
		Description: "Project details including workflow states. identifier is the short code e.g. ENG.",
	}, projectDetailHandler(reader, r, allTypes))

	projectStatesTpl := &mcpmcp.URITemplate{
		Template: uritemplate.MustNew("tack://project/{identifier}/states"),
	}
	s.AddResourceTemplate(mcpmcp.ResourceTemplate{
		Name:        "tack-project-states",
		URITemplate: projectStatesTpl,
		MIMEType:    "application/json",
		Description: "Workflow states for a project, with group names and colors. Use state IDs from this list when creating or updating issues.",
	}, projectStatesHandler(reader, r, allTypes))
}

// ── workspaces ────────────────────────────────────────────────────────────────

func workspacesHandler(r *Resolver) mcpserver.ResourceHandlerFunc {
	return func(ctx context.Context, req mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
		telemetry.L(ctx).Debug("resource.workspaces")
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
	r *Resolver,
	allTypes []*node.NodeType,
) mcpserver.ResourceTemplateHandlerFunc {
	typeIndex := node.BuildTypeIndex(allTypes)
	var entryPointType *node.NodeType
	for _, nt := range allTypes {
		if nt.Features.Has(node.FeatureIsEntryPoint) {
			entryPointType = nt
			break
		}
	}

	return func(ctx context.Context, req mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
		slug := strings.TrimPrefix(req.Params.URI, "tack://workspace/")
		telemetry.L(ctx).Debug("resource.workspace", slog.String("slug", slug))
		if slug == "" || strings.Contains(slug, "/") {
			return nil, errors.New("not found")
		}
		ws, err := r.Workspace(ctx, slug)
		if err != nil {
			return nil, errors.New("not found")
		}

		result := map[string]any{"entry_point": ws}

		if entryPointType != nil {
			addContainerChildren(ctx, reader, ws.OrgID, ws.ID, uuid.Nil, entryPointType, typeIndex, result)
		}

		body, _ := json.Marshal(result)
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
	allTypes []*node.NodeType,
) mcpserver.ResourceTemplateHandlerFunc {
	typeIndex := node.BuildTypeIndex(allTypes)

	return func(ctx context.Context, req mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
		identifier := strings.TrimPrefix(req.Params.URI, "tack://project/")
		telemetry.L(ctx).Debug("resource.project", slog.String("identifier", identifier))
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
			result := map[string]any{
				"container":   proj,
				"entry_point": map[string]any{"id": ws.ID, "slug": ViewSlug(ws), "name": ws.Name},
			}
			scopeType := typeIndex[proj.NodeType]
			if scopeType != nil {
				addContainerChildren(ctx, reader, ws.OrgID, ws.ID, proj.ID, scopeType, typeIndex, result)
			}
			body, _ := json.Marshal(result)
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
	allTypes []*node.NodeType,
) mcpserver.ResourceTemplateHandlerFunc {
	typeIndex := node.BuildTypeIndex(allTypes)

	return func(ctx context.Context, req mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
		path := strings.TrimPrefix(req.Params.URI, "tack://project/")
		identifier := strings.TrimSuffix(path, "/states")
		telemetry.L(ctx).Debug("resource.container_children", slog.String("identifier", identifier))
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
			result := map[string]any{"container_identifier": identifier}
			scopeType := typeIndex[proj.NodeType]
			if scopeType != nil {
				addContainerChildren(ctx, reader, ws.OrgID, ws.ID, proj.ID, scopeType, typeIndex, result)
			}
			body, _ := json.Marshal(result)
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
