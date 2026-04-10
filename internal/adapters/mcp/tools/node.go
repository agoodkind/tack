package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"goodkind.io/tack/internal/domain/node"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterNodeType generates MCP tools for a custom node type based on its AllowedOps.
// Tool names follow the pattern: tack_list_<slug>s, tack_get_<slug>, tack_create_<slug>, etc.
func RegisterNodeType(s *mcp.Server, nt *node.NodeType, properties node.PropertyRepository, activity node.ActivityRepository) {
	slug := strings.ToLower(nt.Slug)
	allowedOps := opSet(nt.AllowedOps)

	if _, ok := allowedOps[node.OpList]; ok {
		mcp.AddTool(s, &mcp.Tool{
			Name:        fmt.Sprintf("tack_list_%ss", slug),
			Description: fmt.Sprintf("List %s nodes. Custom type defined in this workspace.", nt.Name),
		}, listNodes(nt, properties))
	}

	if _, ok := allowedOps[node.OpRead]; ok {
		mcp.AddTool(s, &mcp.Tool{
			Name:        fmt.Sprintf("tack_get_%s", slug),
			Description: fmt.Sprintf("Get a %s node by its node_id.", nt.Name),
		}, getNode(nt, properties))
	}

	if _, ok := allowedOps[node.OpCreate]; ok {
		mcp.AddTool(s, &mcp.Tool{
			Name:        fmt.Sprintf("tack_create_%s", slug),
			Description: fmt.Sprintf("Create a new %s node.", nt.Name),
		}, createNode(nt, properties, activity))
	}

	if _, ok := allowedOps[node.OpUpdate]; ok {
		mcp.AddTool(s, &mcp.Tool{
			Name:        fmt.Sprintf("tack_update_%s", slug),
			Description: fmt.Sprintf("Update a %s node's custom properties (partial).", nt.Name),
		}, updateNode(nt, properties, activity))
	}

	if _, ok := allowedOps[node.OpDelete]; ok {
		mcp.AddTool(s, &mcp.Tool{
			Name:        fmt.Sprintf("tack_delete_%s", slug),
			Description: fmt.Sprintf("Delete a %s node.", nt.Name),
		}, deleteNode(nt, properties, activity))
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

// ── list ─────────────────────────────────────────────────────────────────────

type NodeListInput struct {
	OrgID       string `json:"org_id"`
	WorkspaceID string `json:"workspace_id"`
}

func listNodes(nt *node.NodeType, properties node.PropertyRepository) mcp.ToolHandlerFor[NodeListInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in NodeListInput) (*mcp.CallToolResult, any, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return nil, nil, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, nil, err
		}
		defs, err := properties.ListDefs(ctx, orgID, wsID, nil)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{
			"type":      nt,
			"prop_defs": defs,
			"note":      "node instances are stored in FDB — use tack_get_properties with a node_id to fetch values",
		}, nil
	}
}

// ── get ──────────────────────────────────────────────────────────────────────

type NodeGetInput struct {
	OrgID  string `json:"org_id"`
	NodeID string `json:"node_id"`
}

func getNode(nt *node.NodeType, properties node.PropertyRepository) mcp.ToolHandlerFor[NodeGetInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in NodeGetInput) (*mcp.CallToolResult, any, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return nil, nil, err
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return nil, nil, err
		}
		vals, err := properties.GetValues(ctx, orgID, nodeID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{
			"node_id":    nodeID.String(),
			"type_name":  nt.Name,
			"properties": vals,
		}, nil
	}
}

// ── create ───────────────────────────────────────────────────────────────────

type NodeCreateInput struct {
	OrgID       string         `json:"org_id,omitempty"`
	WorkspaceID string         `json:"workspace_id"`
	Properties  map[string]any `json:"properties,omitempty"`
}
type NodeCreateOutput struct {
	NodeID   string `json:"node_id"`
	TypeName string `json:"type_name"`
}

func createNode(nt *node.NodeType, properties node.PropertyRepository, activity node.ActivityRepository) mcp.ToolHandlerFor[NodeCreateInput, NodeCreateOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in NodeCreateInput) (*mcp.CallToolResult, NodeCreateOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, NodeCreateOutput{}, err
		}
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return nil, NodeCreateOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, NodeCreateOutput{}, err
		}
		nodeID := uuid.New()
		for defIDStr, val := range in.Properties {
			defID, err := parseUUID(defIDStr, "property_def_id")
			if err != nil {
				continue
			}
			_ = properties.SetValue(ctx, orgID, nodeID, defID, val)
		}
		_ = activity.Append(ctx, orgID, wsID, &node.ActivityEvent{
			EventID:   uuid.New(),
			NodeID:    nodeID,
			Actor:     userID,
			Verb:      "created",
			Detail:    map[string]any{"type": nt.Name},
			CreatedAt: time.Now().UTC(),
		})
		return nil, NodeCreateOutput{NodeID: nodeID.String(), TypeName: nt.Name}, nil
	}
}

// ── update ───────────────────────────────────────────────────────────────────

type NodeUpdateInput struct {
	OrgID      string         `json:"org_id,omitempty"`
	NodeID     string         `json:"node_id,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}
type NodeUpdateOutput struct {
	OK bool `json:"ok"`
}

func updateNode(nt *node.NodeType, properties node.PropertyRepository, activity node.ActivityRepository) mcp.ToolHandlerFor[NodeUpdateInput, NodeUpdateOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in NodeUpdateInput) (*mcp.CallToolResult, NodeUpdateOutput, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return nil, NodeUpdateOutput{}, err
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return nil, NodeUpdateOutput{}, err
		}
		for defIDStr, val := range in.Properties {
			defID, err := parseUUID(defIDStr, "property_def_id")
			if err != nil {
				return nil, NodeUpdateOutput{}, fmt.Errorf("invalid property_def_id %q: %w", defIDStr, err)
			}
			if err := properties.SetValue(ctx, orgID, nodeID, defID, val); err != nil {
				return nil, NodeUpdateOutput{}, err
			}
		}
		return nil, NodeUpdateOutput{OK: true}, nil
	}
}

// ── delete ───────────────────────────────────────────────────────────────────

type NodeDeleteInput struct {
	OrgID       string `json:"org_id"`
	WorkspaceID string `json:"workspace_id"`
	NodeID      string `json:"node_id"`
}
type NodeDeleteOutput struct {
	OK bool `json:"ok"`
}

func deleteNode(nt *node.NodeType, properties node.PropertyRepository, activity node.ActivityRepository) mcp.ToolHandlerFor[NodeDeleteInput, NodeDeleteOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in NodeDeleteInput) (*mcp.CallToolResult, NodeDeleteOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, NodeDeleteOutput{}, err
		}
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return nil, NodeDeleteOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, NodeDeleteOutput{}, err
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return nil, NodeDeleteOutput{}, err
		}
		vals, err := properties.GetValues(ctx, orgID, nodeID)
		if err != nil {
			return nil, NodeDeleteOutput{}, err
		}
		for defID := range vals {
			_ = properties.DeleteValue(ctx, orgID, nodeID, defID)
		}
		_ = activity.Append(ctx, orgID, wsID, &node.ActivityEvent{
			EventID:   uuid.New(),
			NodeID:    nodeID,
			Actor:     userID,
			Verb:      "deleted",
			Detail:    map[string]any{"type": nt.Name},
			CreatedAt: time.Now().UTC(),
		})
		return nil, NodeDeleteOutput{OK: true}, nil
	}
}
