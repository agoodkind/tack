package foundationdb

import (
	"context"
	"slices"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// initializeSearchAccess records the authority's current write versions for
// a node that has no access state yet. Its keys stay empty until the first
// access work compiles and records them. Content pages compile keys for
// these versions when they are read.
func initializeSearchAccess(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) error {
	var existing searchAccessRecord
	found, err := readSearchRecord(ctx, tr, searchAccessKey(orgID, nodeID), &existing)
	if err != nil || found {
		return err
	}
	rollout, err := readRollout(ctx, tr, orgID)
	if err != nil {
		return err
	}
	return writeSearchRecord(ctx, tr, searchAccessKey(orgID, nodeID), searchAccessRecord{
		Access:       node.SearchAccess{Versions: slices.Clone(rollout.WriteVersions), Keys: []string{}, Generation: 0},
		PagesPending: false, DependentsPending: false,
	})
}

// scheduleRelatedSearchWork schedules access work for both endpoints of every
// changed relationship and records one permission event for each changed
// authority. The access worker uses the registered policy to compile each
// endpoint. This code never interprets a relationship type.
func scheduleRelatedSearchWork(ctx context.Context, tr fdb.Transaction, now time.Time, changes []node.RelationshipChanges) error {
	scheduled := make(map[[2]uuid.UUID]struct{})
	authorities := make(map[uuid.UUID]struct{})
	for _, change := range changes {
		relationships := append(append([]*node.Relationship{}, change.Add...), change.Remove...)
		for _, relationship := range relationships {
			if relationship == nil {
				continue
			}
			if _, recorded := authorities[relationship.OrgID]; !recorded {
				authorities[relationship.OrgID] = struct{}{}
				addPermissionEvent(tr, relationship.OrgID)
			}
			for _, nodeID := range []uuid.UUID{relationship.SourceID, relationship.TargetID} {
				identity := [2]uuid.UUID{relationship.OrgID, nodeID}
				if _, exists := scheduled[identity]; exists || nodeID == uuid.Nil {
					continue
				}
				scheduled[identity] = struct{}{}
				if err := scheduleExistingSearchChange(ctx, tr, now, relationship.OrgID, nodeID, searchChangeAccess); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// scheduleExistingSearchChange schedules work only while the node's
// resolution record exists in this transaction.
func scheduleExistingSearchChange(ctx context.Context, tr fdb.Transaction, now time.Time, orgID, nodeID uuid.UUID, change searchChange) error {
	resolved, err := tr.Get(fdb.Key(nodeResolveKey(nodeID))).Get()
	if err != nil {
		return searchReadFailure(ctx, "read node resolution "+nodeID.String()+" for search scheduling", err)
	}
	if len(resolved) == 0 {
		return nil
	}
	_, err = scheduleSearchChange(ctx, tr, now, orgID, nodeID, change)
	return err
}
