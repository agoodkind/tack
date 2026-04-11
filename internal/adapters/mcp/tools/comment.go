package tools

import (
	"context"
	"time"

	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/workspace"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterComment registers tack_list_comments and tack_create_comment.
func RegisterComment(s *mcp.Server, workspaces workspace.Repository, comments node.CommentRepository) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_list_comments",
		Description: "List all comments on a node (issue, epic, etc.) in chronological order. Provide the workspace slug and the node ID (UUID). Returns comment body, author, and timestamps.",
	}, listComments(workspaces, comments))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_create_comment",
		Description: "Add a comment to a node (issue, epic, etc.). Provide the workspace slug and the node ID (UUID) of the entity to comment on, along with the comment body (Markdown supported).",
	}, createComment(workspaces, comments))
}

// ── list_comments ─────────────────────────────────────────────────────────────

type ListCommentsInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
	NodeID        string `json:"node_id"`
}

func listComments(workspaces workspace.Repository, comments node.CommentRepository) mcp.ToolHandlerFor[ListCommentsInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListCommentsInput) (*mcp.CallToolResult, any, error) {
		ws, err := workspaces.GetBySlug(ctx, in.WorkspaceSlug)
		if err != nil {
			return nil, nil, err
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return nil, nil, err
		}
		cs, err := comments.List(ctx, ws.OrgID, nodeID)
		if err != nil {
			return nil, nil, err
		}
		if cs == nil {
			cs = []*node.Comment{}
		}
		return nil, map[string]any{"comments": cs, "total": len(cs)}, nil
	}
}

// ── create_comment ───────────────────────────────────────────────────────────

type CreateCommentInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
	NodeID        string `json:"node_id"`
	Body          string `json:"body"`
}

func createComment(workspaces workspace.Repository, comments node.CommentRepository) mcp.ToolHandlerFor[CreateCommentInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateCommentInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, nil, err
		}
		ws, err := workspaces.GetBySlug(ctx, in.WorkspaceSlug)
		if err != nil {
			return nil, nil, err
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return nil, nil, err
		}
		comment := &node.Comment{
			ID:        uuid.New(),
			NodeID:    nodeID,
			Body:      in.Body,
			AuthorID:  userID,
			CreatedAt: time.Now().UTC(),
		}
		if err := comments.Create(ctx, ws.OrgID, comment); err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"comment": comment}, nil
	}
}
