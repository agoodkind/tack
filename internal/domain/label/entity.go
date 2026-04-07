package label

import (
	"time"

	"github.com/google/uuid"
)

type Label struct {
	ID          uuid.UUID  `json:"id"`
	WorkspaceID uuid.UUID  `json:"workspace_id"`
	ProjectID   *uuid.UUID `json:"project_id"` // nil = workspace-level label
	ParentID    *uuid.UUID `json:"parent_id"`
	Name        string     `json:"name"`
	Color       string     `json:"color"`
	SortOrder   float64    `json:"sort_order"`
	CreatedBy   uuid.UUID  `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}
