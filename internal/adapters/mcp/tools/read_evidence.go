package tools

import (
	"context"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/user"
)

// addNodeTargets adds an audit read target for each non-nil node view.
func addNodeTargets(ctx context.Context, views ...*node.NodeView) {
	for _, view := range views {
		if view == nil {
			continue
		}
		audit.AddReadTargets(ctx, audit.Target{
			Kind: audit.TargetKindNode, NodeType: view.NodeType, Reference: "", Name: "", URI: "",
			RelationType: "", ID: view.ID, SourceID: uuid.Nil, TargetID: uuid.Nil,
		})
	}
}

// addRelationshipTargets adds an audit read target for each non-nil relationship.
func addRelationshipTargets(ctx context.Context, relationships []*node.Relationship) {
	for _, relationship := range relationships {
		if relationship == nil {
			continue
		}
		audit.AddReadTargets(ctx, audit.Target{
			Kind: audit.TargetKindRelationship, NodeType: "", Reference: "", Name: "", URI: "",
			RelationType: relationship.RelationType, ID: uuid.Nil,
			SourceID: relationship.SourceID, TargetID: relationship.TargetID,
		})
	}
}

// userTargetPrefix is the URI prefix for user audit read targets.
const userTargetPrefix = "tack://user/"

// addUserTargets adds an audit read target for each user.
func addUserTargets(ctx context.Context, users []*user.User) {
	for _, member := range users {
		audit.AddReadTargets(ctx, audit.Target{
			Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: "",
			URI: userTargetPrefix + member.ID.String(), RelationType: "",
			ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
		})
	}
}

// noteToolOutcome stores an unsuccessful tool call's outcome for the request's read event.
// A note from the handler takes precedence.
func noteToolOutcome(ctx context.Context, refused bool, runErr error, result *mcpmcp.CallToolResult) {
	switch {
	case refused:
		audit.NoteReadOutcome(ctx, audit.OutcomeRefused, "permission_denied",
			"The tool result did not pass a membership check.")
	case runErr != nil:
		audit.NoteReadOutcome(ctx, audit.OutcomeFailed, "tool_failed", "The tool handler returned an error.")
	case result != nil && result.IsError:
		audit.NoteReadOutcome(ctx, audit.OutcomeRefused, "tool_request_rejected",
			"The tool rejected the request arguments or target.")
	}
}
