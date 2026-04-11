package tools

import (
	"context"
	"time"

	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/workspace"
	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// RegisterComment registers tack_list_comments and tack_create_comment.
func RegisterComment(s *mcpserver.MCPServer, workspaces workspace.Repository, comments node.CommentRepository) {
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_list_comments",
		Description: "Lists all comments on a node (issue, epic, etc.) in chronological order. Returns comment body, author_id, and timestamps.",
		InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string", "description": "The workspace slug"}, "node_id": map[string]any{"type": "string", "description": "The node ID"}}, Required: []string{"workspace_slug", "node_id"}},
	}, listComments(workspaces, comments))

	s.AddTool(mcpmcp.Tool{
		Name:        "tack_create_comment",
		Description: "Adds a comment to a node. Body supports Markdown. Returns the created comment.",
		InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string", "description": "The workspace slug"}, "node_id": map[string]any{"type": "string", "description": "The node ID"}, "body": map[string]any{"type": "string", "description": "The comment body"}}, Required: []string{"workspace_slug", "node_id", "body"}},
	}, createComment(workspaces, comments))
}

// ── list_comments ─────────────────────────────────────────────────────────────

type ListCommentsInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
	NodeID        string `json:"node_id"`
}

func listComments(workspaces workspace.Repository, comments node.CommentRepository) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in ListCommentsInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, err := workspaces.GetBySlug(ctx, in.WorkspaceSlug)
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

func createComment(workspaces workspace.Repository, comments node.CommentRepository) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in CreateCommentInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		ws, err := workspaces.GetBySlug(ctx, in.WorkspaceSlug)
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
