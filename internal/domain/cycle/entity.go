package cycle

import (
	"time"

	"github.com/google/uuid"
)

type Cycle struct {
	ID              uuid.UUID  `json:"id"`
	NodeID          uuid.UUID  `json:"node_id"`
	WorkspaceID     uuid.UUID  `json:"workspace_id"`
	ProjectID       uuid.UUID  `json:"project_id"`
	Name            string     `json:"name"`
	Description     string     `json:"description"`
	StartDate       *time.Time `json:"start_date"`
	EndDate         *time.Time `json:"end_date"`
	SortOrder       float64    `json:"sort_order"`
	CompletedPoints int        `json:"completed_points"`
	CreatedBy       uuid.UUID  `json:"created_by"`
	UpdatedBy       *uuid.UUID `json:"updated_by"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ArchivedAt      *time.Time `json:"archived_at,omitempty"`
}
