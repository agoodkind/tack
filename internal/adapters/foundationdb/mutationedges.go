package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// readOutgoingSnapshots reads every relationship of relationType from nodeID
// inside transaction. It reads every type when relationType is empty.
func readOutgoingSnapshots(
	ctx context.Context,
	transaction fdb.Transaction,
	orgID uuid.UUID,
	nodeID uuid.UUID,
	relationType string,
) ([]node.RelationshipSnapshot, error) {
	keyRange, err := fdb.PrefixRange(relationshipPrefixBySource(orgID, nodeID, relationType))
	if err != nil {
		return nil, nodeOperationFailure(
			ctx,
			"create relationship range for node "+nodeID.String(),
			err,
		)
	}

	items, err := transaction.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(ctx, "read relationships of node "+nodeID.String(), err)
	}

	snapshots := make([]node.RelationshipSnapshot, 0, len(items))
	for _, item := range items {
		snapshot, err := decodeRelationshipSnapshot(ctx, item.Value)
		if err != nil {
			return nil, err
		}

		snapshots = append(snapshots, snapshot)
	}

	return snapshots, nil
}

// readIncomingSnapshots reads relationships to nodeID inside transaction. If
// their
// count exceeds node.MutationEdgeLimit, it reports true and does not return
// relationships. readDeleteEdges reads more than that limit.
func readIncomingSnapshots(
	ctx context.Context,
	transaction fdb.Transaction,
	orgID uuid.UUID,
	nodeID uuid.UUID,
) ([]node.RelationshipSnapshot, bool, error) {
	edges, err := readDeleteEdges(ctx, transaction, orgID, nodeID, "", true)
	if err != nil {
		return nil, false, err
	}

	if len(edges) > node.MutationEdgeLimit {
		return nil, true, nil
	}

	futures := make([]fdb.FutureByteSlice, 0, len(edges))
	for _, edge := range edges {
		futures = append(
			futures,
			transaction.Get(
				fdb.Key(relationshipKey(orgID, edge.SourceID, edge.RelationType, edge.TargetID)),
			),
		)
	}

	snapshots := make([]node.RelationshipSnapshot, 0, len(edges))

	for position, future := range futures {
		metadata, err := future.Get()
		if err != nil {
			return nil, false, searchReadFailure(
				ctx,
				"read relationship from "+edges[position].SourceID.String(),
				err,
			)
		}

		snapshot, err := decodeRelationshipSnapshot(ctx, metadata)
		if err != nil {
			return nil, false, err
		}

		snapshots = append(snapshots, snapshot)
	}

	return snapshots, false, nil
}

// decodeRelationshipSnapshot decodes the stored metadata of one relationship
// and returns the relationship with its digest.
func decodeRelationshipSnapshot(
	ctx context.Context,
	metadata []byte,
) (node.RelationshipSnapshot, error) {
	var relationship node.Relationship
	if err := json.Unmarshal(metadata, &relationship); err != nil {
		return node.RelationshipSnapshot{}, nodeOperationFailure(
			ctx,
			"decode relationship metadata",
			err,
		)
	}

	return node.RelationshipSnapshot{
		Relationship: relationship,
		Digest:       node.RelationshipDigest(metadata),
	}, nil
}
