package label

import (
	"time"

	"github.com/google/uuid"
)

type Label struct {
	ID          uuid.UUID  `json:"id"`
	WorkspaceID uuid.UUID  `json:"workspace_id"`
	ProjectID   *uuid.UUID `json:"project_id"` // nil = workspace-scoped
	Name        string     `json:"name"`
	Color       string     `json:"color"`
	SortOrder   float64    `json:"sort_order"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}
