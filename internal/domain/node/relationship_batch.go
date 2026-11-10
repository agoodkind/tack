package node

import (
	"context"

	"github.com/google/uuid"
)

// RelationshipBatchLister lists relationships for several source nodes in one
// organization with one read.
type RelationshipBatchLister interface {
	// ListBySources returns relationships keyed by source ID and filtered by
	// relationType. An empty relationType selects every relationship type.
	// If a source does not have relationships, its entry is empty or absent.
	ListBySources(ctx context.Context, orgID uuid.UUID, sourceIDs []uuid.UUID, relationType string) (map[uuid.UUID][]*Relationship, error)
}
