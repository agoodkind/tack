package state

import (
	"time"

	"github.com/google/uuid"
)

// GroupName controls workflow semantics — Scrum status categories.
type GroupName string

const (
	GroupBacklog    GroupName = "backlog"
	GroupUnstarted  GroupName = "unstarted"
	GroupStarted    GroupName = "started"
	GroupCompleted  GroupName = "completed"
	GroupCancelled  GroupName = "cancelled"
)

type State struct {
	ID          uuid.UUID  `json:"id"`
	WorkspaceID uuid.UUID  `json:"workspace_id"`
	ProjectID   uuid.UUID  `json:"project_id"`
	Name        string     `json:"name"`
	Color       string     `json:"color"`
	Group       GroupName  `json:"group"`
	Sequence    float64    `json:"sequence"`
	IsDefault   bool       `json:"is_default"`
	CreatedBy   uuid.UUID  `json:"created_by"`
	UpdatedBy   *uuid.UUID `json:"updated_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}
