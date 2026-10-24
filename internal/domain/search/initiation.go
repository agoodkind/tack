package search

import "github.com/google/uuid"

// Initiation identifies the operator operation and actor that began an index
// replacement or an access policy rollout. The search worker records later
// steps of that work under this operation. The zero value means that a
// record does not identify its initiating operation.
type Initiation struct {
	OperationID uuid.UUID
	ActorID     uuid.UUID
}
