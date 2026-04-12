package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// NodeTypeBinding holds service references for dispatching type-specific tool handlers.
// The TypeKey on each NodeType determines which service field is used.
type NodeTypeBinding struct {
	IssueSvc    *service.IssueService
	EpicSvc     *service.EpicService
	CycleSvc    *service.CycleService
	ModuleSvc   *service.ModuleService
	Properties  node.PropertyRepository
	Activity    node.ActivityRepository
	Containment node.ContainmentRepository
	Entities    node.EntityRepository
	Reader      node.NodeReader
	Resolver    *Resolver
}

// RegisterNodeTools registers all MCP tools for a single NodeType.
// Tool names are derived from nt.Slug and nt.PluralSlug.
// Dispatch is by nt.TypeKey: builtin types with dedicated services go to their
// service; all other types (including state, label, and custom types) use the
// generic entity read/write path.
func RegisterNodeTools(s *mcpserver.MCPServer, nt *node.NodeType, b NodeTypeBinding) {
	plural := nt.PluralSlug
	if plural == "" {
		plural = nt.Slug + "s"
	}
	switch nt.TypeKey {
	case node.NodeTypeIssue:
		registerIssueTools(s, nt.Slug, plural, b.IssueSvc, b.Resolver)
	case node.NodeTypeEpic:
		registerEpicTools(s, nt.Slug, plural, b.EpicSvc, b.Resolver)
	case node.NodeTypeCycle:
		registerCycleTools(s, nt.Slug, plural, b.CycleSvc, b.Containment, b.Resolver)
	case node.NodeTypeModule:
		registerModuleTools(s, nt.Slug, plural, b.ModuleSvc, b.Containment, b.Resolver)
	case node.NodeTypeOrg, node.NodeTypeWorkspace, node.NodeTypeProject:
		// Container types with dedicated structural registrations. Skip.
		return
	default:
		registerGenericNodeTools(s, nt, b)
	}
}

// nodeParentScope returns "project" if CanLiveUnder includes "project", else "workspace".
func nodeParentScope(nt *node.NodeType) string {
	for _, parent := range nt.CanLiveUnder {
		if parent == node.NodeTypeProject {
			return "project"
		}
	}
	return "workspace"
}

// registerGenericNodeTools generates CRUD MCP tools for any node type that does
// not have a dedicated service (state, label, and all user-defined custom types).
func registerGenericNodeTools(s *mcpserver.MCPServer, nt *node.NodeType, b NodeTypeBinding) {
	slug := strings.ToLower(nt.Slug)
	plural := strings.ToLower(nt.PluralSlug)
	if plural == "" {
		plural = slug + "s"
	}
	scope := nodeParentScope(nt)
	ops := opSet(nt.AllowedOps)

	if _, ok := ops[node.OpList]; ok {
		s.AddTool(genericListTool(nt, plural, scope), genericListHandler(nt, scope, b))
	}
	if _, ok := ops[node.OpCreate]; ok {
		s.AddTool(genericCreateTool(nt, slug, scope), genericCreateHandler(nt, scope, b))
	}
	if _, ok := ops[node.OpRead]; ok {
		s.AddTool(genericGetTool(nt, slug), genericGetHandler(nt, b.Reader))
	}
	if _, ok := ops[node.OpUpdate]; ok {
		s.AddTool(genericUpdateTool(nt, slug), genericUpdateHandler(nt, b))
	}
	if _, ok := ops[node.OpDelete]; ok {
		s.AddTool(genericDeleteTool(nt, slug), genericDeleteHandler(nt, b))
	}
}

// ── tool schema builders ────────────────────────────────────────────────────

func genericListTool(nt *node.NodeType, plural, scope string) mcpmcp.Tool {
	props := map[string]any{
		"workspace_slug": map[string]any{"type": "string", "description": "The workspace slug"},
	}
	required := []string{"workspace_slug"}
	if scope == "project" {
		props["project_identifier"] = map[string]any{"type": "string", "description": "The project identifier (e.g. TACK)"}
		required = append(required, "project_identifier")
	}
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_list_%s", plural),
		Description: fmt.Sprintf("Lists all %s in the given scope. Returns name, id, sort_order, and properties.", plural),
		InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: props, Required: required},
	}
}

func genericCreateTool(nt *node.NodeType, slug, scope string) mcpmcp.Tool {
	props := map[string]any{
		"workspace_slug": map[string]any{"type": "string", "description": "The workspace slug"},
		"name":           map[string]any{"type": "string", "description": fmt.Sprintf("The %s name", nt.Name)},
		"properties":     map[string]any{"type": "object", "description": "Property values keyed by property name (e.g. {\"group_name\": \"started\", \"color\": \"#ff0000\"})"},
	}
	required := []string{"workspace_slug", "name"}
	if scope == "project" {
		props["project_identifier"] = map[string]any{"type": "string", "description": "The project identifier (e.g. TACK)"}
		required = append(required, "project_identifier")
	}
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_create_%s", slug),
		Description: fmt.Sprintf("Creates a new %s. Pass custom fields in the properties object keyed by property name.", nt.Name),
		InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: props, Required: required},
	}
}

func genericGetTool(nt *node.NodeType, slug string) mcpmcp.Tool {
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_get_%s", slug),
		Description: fmt.Sprintf("Fetches a %s by its ID.", nt.Name),
		InputSchema: mcpmcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{"node_id": map[string]any{"type": "string", "description": "The node UUID"}},
			Required:   []string{"node_id"},
		},
	}
}

func genericUpdateTool(nt *node.NodeType, slug string) mcpmcp.Tool {
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_update_%s", slug),
		Description: fmt.Sprintf("Updates a %s. Only provided fields change.", nt.Name),
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"node_id":    map[string]any{"type": "string", "description": "The node UUID"},
				"name":       map[string]any{"type": "string", "description": "New name"},
				"properties": map[string]any{"type": "object", "description": "Property values to update keyed by property name"},
			},
			Required: []string{"node_id"},
		},
	}
}

func genericDeleteTool(nt *node.NodeType, slug string) mcpmcp.Tool {
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_delete_%s", slug),
		Description: fmt.Sprintf("Deletes a %s.", nt.Name),
		InputSchema: mcpmcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{"node_id": map[string]any{"type": "string", "description": "The node UUID"}},
			Required:   []string{"node_id"},
		},
	}
}

// ── handlers ─────────────────────────────────────────────────────────────────

type genericListInput struct {
	WorkspaceSlug     string  `json:"workspace_slug"`
	ProjectIdentifier *string `json:"project_identifier,omitempty"`
}

func genericListHandler(nt *node.NodeType, scope string, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in genericListInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, err := b.Resolver.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		q := node.NodeListQuery{
			OrgID:       ws.OrgID,
			WorkspaceID: ws.ID,
			NodeType:    nt.TypeKey,
		}
		if scope == "project" {
			if in.ProjectIdentifier == nil || *in.ProjectIdentifier == "" {
				return RecoverableError("project_identifier is required"), nil
			}
			_, proj, err := b.Resolver.Project(ctx, in.WorkspaceSlug, *in.ProjectIdentifier)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			q.ByProject = &proj.ID
		}
		views, err := b.Reader.List(ctx, q)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		sort.Slice(views, func(i, j int) bool {
			return views[i].SortOrder < views[j].SortOrder
		})
		plural := nt.PluralSlug
		if plural == "" {
			plural = nt.Slug + "s"
		}
		return Success(map[string]any{plural: views}, ""), nil
	}
}

type genericCreateInput struct {
	WorkspaceSlug     string         `json:"workspace_slug"`
	ProjectIdentifier *string        `json:"project_identifier,omitempty"`
	Name              string         `json:"name"`
	Properties        map[string]any `json:"properties,omitempty"`
}

func genericCreateHandler(nt *node.NodeType, scope string, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in genericCreateInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		ws, err := b.Resolver.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		orgID := ws.OrgID
		wsID := ws.ID
		var projID uuid.UUID
		if scope == "project" {
			if in.ProjectIdentifier == nil || *in.ProjectIdentifier == "" {
				return RecoverableError("project_identifier is required"), nil
			}
			_, proj, err := b.Resolver.Project(ctx, in.WorkspaceSlug, *in.ProjectIdentifier)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			projID = proj.ID
		}

		// Resolve PropertyDefs by name so callers pass names, not UUIDs.
		defs, _ := b.Properties.ListDefs(ctx, orgID, wsID, nil)
		defsByName := make(map[string]*node.PropertyDef, len(defs))
		for _, d := range defs {
			defsByName[strings.ToLower(d.Name)] = d
		}

		now := time.Now()
		id := uuid.New()

		props := make(map[uuid.UUID]*node.PropertyValue)
		customProps := make(map[string]json.RawMessage)

		for name, val := range in.Properties {
			def, ok := defsByName[strings.ToLower(name)]
			if !ok {
				continue
			}
			valStr := fmt.Sprintf("%v", val)
			pv := stringToPropertyValue(valStr, def.Type)
			if pv != nil {
				props[def.ID] = pv
				raw, _ := json.Marshal(val)
				customProps[name] = raw
			}
		}

		nv := &node.NodeValue{
			ID:          id,
			OrgID:       orgID,
			WorkspaceID: wsID,
			ProjectID:   projID,
			NodeType:    nt.TypeKey,
			Name:        in.Name,
			SortOrder:   65535,
			CreatedBy:   userID,
			UpdatedBy:   userID,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		view := &node.NodeListView{
			Version:     node.ViewVersion1,
			ID:          id,
			OrgID:       orgID,
			WorkspaceID: wsID,
			ProjectID:   projID,
			NodeType:    nt.TypeKey,
			Name:        in.Name,
			SortOrder:   65535,
			CreatedBy:   userID,
			UpdatedBy:   userID,
			CreatedAt:   now,
			UpdatedAt:   now,
			CustomProps: customProps,
		}

		if _, err := b.Entities.CreateAtomic(ctx, orgID, projID, nv, props, view, nil, nil, userID); err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{nt.Slug: view}, ""), nil
	}
}

type genericGetInput struct {
	NodeID string `json:"node_id"`
}

func genericGetHandler(nt *node.NodeType, reader node.NodeReader) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in genericGetInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		id, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		view, err := reader.Get(ctx, id)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		if view == nil {
			return ClassifyError(ctx, domain.ErrNotFound), nil
		}
		return Success(map[string]any{nt.Slug: view}, ""), nil
	}
}

type genericUpdateInput struct {
	NodeID     string         `json:"node_id"`
	Name       *string        `json:"name,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

func genericUpdateHandler(nt *node.NodeType, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in genericUpdateInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		id, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		view, err := b.Reader.Get(ctx, id)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		if view == nil {
			return ClassifyError(ctx, domain.ErrNotFound), nil
		}
		resolve, err := b.Reader.Resolve(ctx, id)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}

		now := time.Now()
		if in.Name != nil {
			view.Name = *in.Name
		}
		view.UpdatedAt = now

		// Resolve PropertyDefs by name.
		defs, _ := b.Properties.ListDefs(ctx, resolve.OrgID, resolve.WorkspaceID, nil)
		defsByName := make(map[string]*node.PropertyDef, len(defs))
		for _, d := range defs {
			defsByName[strings.ToLower(d.Name)] = d
		}

		props := make(map[uuid.UUID]*node.PropertyValue)
		if view.CustomProps == nil {
			view.CustomProps = make(map[string]json.RawMessage)
		}
		for name, val := range in.Properties {
			def, ok := defsByName[strings.ToLower(name)]
			if !ok {
				continue
			}
			valStr := fmt.Sprintf("%v", val)
			pv := stringToPropertyValue(valStr, def.Type)
			if pv != nil {
				props[def.ID] = pv
				raw, _ := json.Marshal(val)
				view.CustomProps[name] = raw
			}
		}

		nv := &node.NodeValue{
			ID:          id,
			OrgID:       resolve.OrgID,
			WorkspaceID: resolve.WorkspaceID,
			ProjectID:   resolve.ProjectID,
			NodeType:    nt.TypeKey,
			Name:        view.Name,
			SortOrder:   view.SortOrder,
			CreatedBy:   view.CreatedBy,
			UpdatedAt:   now,
			CreatedAt:   view.CreatedAt,
		}

		if err := b.Entities.Set(ctx, nv, props, view); err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{nt.Slug: view}, ""), nil
	}
}

type genericDeleteInput struct {
	NodeID string `json:"node_id"`
}

func genericDeleteHandler(nt *node.NodeType, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in genericDeleteInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		id, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		resolve, err := b.Reader.Resolve(ctx, id)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		nv := &node.NodeValue{
			ID:          id,
			OrgID:       resolve.OrgID,
			WorkspaceID: resolve.WorkspaceID,
			ProjectID:   resolve.ProjectID,
			NodeType:    nt.TypeKey,
		}
		if err := b.Entities.Delete(ctx, nv); err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"ok": true}, ""), nil
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func opSet(ops []node.Op) map[node.Op]struct{} {
	m := make(map[node.Op]struct{}, len(ops))
	for _, op := range ops {
		m[op] = struct{}{}
	}
	return m
}

// stringToPropertyValue converts a string input to a typed PropertyValue based
// on the property definition type.
func stringToPropertyValue(val string, propType node.PropertyType) *node.PropertyValue {
	switch propType {
	case node.PropertyTypeText, node.PropertyTypeURL:
		return node.TextPropertyValue(val)
	case node.PropertyTypeSelect:
		return &node.PropertyValue{Kind: node.PropertyValueEnum, Enum: &val}
	case node.PropertyTypeNumber:
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return nil
		}
		return &node.PropertyValue{Kind: node.PropertyValueFloat, Float: &f}
	case node.PropertyTypeCheckbox:
		b := val == "true"
		return &node.PropertyValue{Kind: node.PropertyValueBool, Bool: &b}
	default:
		return node.TextPropertyValue(val)
	}
}
