package tools

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// RegisterComment registers tack_list_comments and tack_create_comment.
func RegisterComment(s *mcpserver.MCPServer, comments node.CommentRepository, r *Resolver) {
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_list_comments",
		Description: "Lists all comments on a node (issue, epic, etc.) in chronological order. Returns comment body, author_id, and timestamps.",
		InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string", "description": "The workspace slug"}, "node_id": map[string]any{"type": "string", "description": "The node ID"}}, Required: []string{"workspace_slug", "node_id"}},
	}, listComments(comments, r))

	s.AddTool(mcpmcp.Tool{
		Name:        "tack_create_comment",
		Description: "Adds a comment to a node. Body supports Markdown. Returns the created comment.",
		InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string", "description": "The workspace slug"}, "node_id": map[string]any{"type": "string", "description": "The node ID"}, "body": map[string]any{"type": "string", "description": "The comment body"}}, Required: []string{"workspace_slug", "node_id", "body"}},
	}, createComment(comments, r))
}

// ── list_comments ─────────────────────────────────────────────────────────────

type ListCommentsInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
	NodeID        string `json:"node_id"`
}

func listComments(comments node.CommentRepository, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in ListCommentsInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		telemetry.L(ctx).Debug("mcp.list_comments", slog.String("node_id", in.NodeID))
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		cs, err := comments.List(ctx, ws.OrgID, nodeID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		if cs == nil {
			cs = []*node.Comment{}
		}
		return Success(map[string]any{"comments": cs, "total": len(cs)}, ""), nil
	}
}

// ── create_comment ───────────────────────────────────────────────────────────

type CreateCommentInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
	NodeID        string `json:"node_id"`
	Body          string `json:"body"`
}

func createComment(comments node.CommentRepository, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in CreateCommentInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		telemetry.L(ctx).Debug("mcp.create_comment", slog.String("node_id", in.NodeID))
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		comment := &node.Comment{
			ID:        uuid.New(),
			NodeID:    nodeID,
			Body:      in.Body,
			AuthorID:  userID,
			CreatedAt: time.Now().UTC(),
		}
		if err := comments.Create(ctx, ws.OrgID, comment); err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"comment": comment}, "Comment added. Use tack_list_comments to see all comments on this node."), nil
	}
}
