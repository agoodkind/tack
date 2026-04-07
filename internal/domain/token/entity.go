package token

import (
	"time"

	"github.com/google/uuid"
)

type Token struct {
	ID          uuid.UUID  `json:"id"`
	UserID      uuid.UUID  `json:"user_id"`
	WorkspaceID *uuid.UUID `json:"workspace_id"`
	Token       string     `json:"-"` // never serialised
	Label       string     `json:"label"`
	IsActive    bool       `json:"is_active"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	CreatedAt   time.Time  `json:"created_at"`
}
