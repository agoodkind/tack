package search

import (
	"encoding/json"

	"github.com/google/uuid"
)

// Initiation identifies the operator operation and actor that began an index
// replacement or an access policy rollout. Operation preserves the original
// command event for its final result. The zero value identifies autonomous work.
type Initiation struct {
	OperationID uuid.UUID
	ActorID     uuid.UUID
	Operation   json.RawMessage `json:"-" exhaustruct:"optional"`
}
