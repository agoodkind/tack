package module

import (
	"time"

	"github.com/google/uuid"
)

type Module struct {
	ID          uuid.UUID  `json:"id"`
	WorkspaceID uuid.UUID  `json:"workspace_id"`
	ProjectID   uuid.UUID  `json:"project_id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Status      string     `json:"status"` // backlog|planned|in_progress|paused|completed|cancelled
	StartDate   *time.Time `json:"start_date"`
	TargetDate  *time.Time `json:"target_date"`
	SortOrder   float64    `json:"sort_order"`
	CreatedBy   uuid.UUID  `json:"created_by"`
	UpdatedBy   *uuid.UUID `json:"updated_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
}
