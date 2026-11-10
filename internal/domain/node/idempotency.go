package node

import (
	"time"

	"github.com/google/uuid"
)

// IdempotencyRecord stores the durable result for one create operation key.
// Legacy records from before fingerprinting omit Fingerprint. The writer also
// accepts an empty fingerprint for new records.
type IdempotencyRecord struct {
	Key         string    `json:"-"`
	NodeID      uuid.UUID `json:"node_id"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	Source      string    `json:"source,omitempty"`
}
