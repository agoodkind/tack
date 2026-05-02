package node

import (
	"time"

	"github.com/google/uuid"
)

// IdempotencyRecord stores the durable result for one create operation key.
// Fingerprint is empty only for legacy records written before fingerprinting.
type IdempotencyRecord struct {
	Key         string    `json:"-"`
	NodeID      uuid.UUID `json:"node_id"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	Source      string    `json:"source,omitempty"`
}
