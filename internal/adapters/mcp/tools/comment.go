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
		Description: "Lists all comments on a node (issue, epic, etc.) in chronological order. Returns comment body, author_id, and timestamps.",
	}, listComments(workspaces, comments))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_create_comment",
		Description: "Adds a comment to a node. Body supports Markdown. Returns the created comment.",
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
			return ClassifyError(ctx, err), nil, nil
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		cs, err := comments.List(ctx, ws.OrgID, nodeID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		if cs == nil {
			cs = []*node.Comment{}
		}
		return Success(map[string]any{"comments": cs, "total": len(cs)}, ""), nil, nil
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
			return UnexpectedError(ctx, err), nil, nil
		}
		ws, err := workspaces.GetBySlug(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		comment := &node.Comment{
			ID:        uuid.New(),
			NodeID:    nodeID,
			Body:      in.Body,
			AuthorID:  userID,
			CreatedAt: time.Now().UTC(),
		}
		if err := comments.Create(ctx, ws.OrgID, comment); err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"comment": comment}, "Comment added. Use tack_list_comments to see all comments on this node."), nil, nil
	}
}
