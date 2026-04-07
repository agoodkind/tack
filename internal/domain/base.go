package domain

import (
	"time"

	"github.com/google/uuid"
)

// Base fields embedded by every core entity.
type Base struct {
	ID        uuid.UUID  `json:"id"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	CreatedBy uuid.UUID  `json:"created_by"`
	UpdatedBy *uuid.UUID `json:"updated_by"`
}
