package workspace

import (
	"time"

	"github.com/google/uuid"
)

type Workspace struct {
	ID        uuid.UUID `json:"id"`
	NodeID    uuid.UUID `json:"node_id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
