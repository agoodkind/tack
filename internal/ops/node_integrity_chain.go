package ops

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// currentChainRecords reads the current primary digest of startID and of
// each parent_id ancestor of startID. It stops reading ancestors at a node
// without a parent or at an ancestor that does not exist. If it detects a
// repeated node, it returns an error that wraps [domain.ErrConflict].
func currentChainRecords(ctx context.Context, mutator node.Mutator, entry NodeIntegrityEntry, orgID, startID uuid.UUID) ([]node.GraphRecord, error) {
	records := []node.GraphRecord{}
	visited := map[uuid.UUID]bool{}
	for currentID := startID; currentID != uuid.Nil; {
		if visited[currentID] {
			return nil, repairRefused(ctx, entry, fmt.Sprintf("the parent chain repeats node %s", currentID), domain.ErrConflict)
		}
		visited[currentID] = true
		snapshot, _, err := readNodeSnapshot(ctx, mutator, entry, orgID, currentID)
		if err != nil {
			return nil, err
		}
		if !snapshot.Exists {
			break
		}
		records = append(records, node.GraphRecord{NodeID: currentID, PrimaryDigest: snapshot.PrimaryDigest})
		currentID = snapshot.ParentID
	}
	sortGraphRecords(records)
	return records, nil
}
