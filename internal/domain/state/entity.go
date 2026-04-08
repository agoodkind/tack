package state

import (
	"time"

	"github.com/google/uuid"
)

type GroupName string

const (
	GroupBacklog   GroupName = "backlog"
	GroupTodo      GroupName = "todo"
	GroupStarted   GroupName = "started"
	GroupCompleted GroupName = "completed"
	GroupCancelled GroupName = "cancelled"
)

type State struct {
	ID        uuid.UUID `json:"id"`
	ProjectID uuid.UUID `json:"project_id"`
	Name      string    `json:"name"`
	GroupName GroupName `json:"group_name"`
	Color     string    `json:"color"`
	SortOrder float64   `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
