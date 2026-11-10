package org

import (
	"time"

	"github.com/google/uuid"
)

type Org struct {
	ID        uuid.UUID `json:"id"`
	NodeID    uuid.UUID `json:"node_id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Member struct {
	ID        uuid.UUID `json:"id"`
	OrgID     uuid.UUID `json:"org_id"`
	UserID    uuid.UUID `json:"user_id"`
	Role      int       `json:"role"` // Role uses 5 for guest, 10 for viewer, 15 for member, and 20 for admin.
	CreatedAt time.Time `json:"created_at"`
}
