package project

import (
	"time"

	"github.com/google/uuid"
)

type Project struct {
	ID             uuid.UUID  `json:"id"`
	NodeID         uuid.UUID  `json:"node_id"`
	WorkspaceID    uuid.UUID  `json:"workspace_id"`
	Name           string     `json:"name"`
	Identifier     string     `json:"identifier"`
	Description    string     `json:"description"`
	Network        int        `json:"network"` // 0=secret, 2=public
	DefaultStateID *uuid.UUID `json:"default_state_id"`
	CreatedBy      uuid.UUID  `json:"created_by"`
	UpdatedBy      *uuid.UUID `json:"updated_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}
