package node

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Comment represents a comment on a node (issue, epic, etc.).
// Stored in FDB under the comment_on_node key space.
type Comment struct {
	ID        uuid.UUID  `json:"id"`
	NodeID    uuid.UUID  `json:"node_id"`
	Body      string     `json:"body"`
	AuthorID  uuid.UUID  `json:"author_id"`
	EditedAt  *time.Time `json:"edited_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// CommentRepository is an append-style store for node comments.
// Comments are keyed by (orgID, nodeID, createdAtNano, commentID) in FDB,
// giving chronological ordering for free.
type CommentRepository interface {
	// Create appends a new comment to a node. comment.ID and comment.CreatedAt
	// must be set by the caller before calling Create.
	Create(ctx context.Context, orgID uuid.UUID, comment *Comment) error
	// List returns all comments on a node, ordered by created_at ascending.
	List(ctx context.Context, orgID, nodeID uuid.UUID) ([]*Comment, error)
}
