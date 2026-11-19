package foundationdb

import (
	"context"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// checkSource compares expected with the source that transaction read. It
// returns an
// error that wraps domain.ErrConflict if they differ.
func checkSource(
	ctx context.Context,
	nodeID uuid.UUID,
	expected node.SourceExpectation,
	source mutationSource,
) error {
	if expected.Absent {
		if source.Exists {
			return mutationRefusal(
				ctx,
				nodeID,
				"the plan expects no node, but the node exists",
				domain.ErrConflict,
			)
		}

		return nil
	}

	if !source.Exists {
		return mutationRefusal(
			ctx,
			nodeID,
			"the plan expects a stored node, but the node does not exist",
			domain.ErrConflict,
		)
	}

	if node.PrimaryDigest(source.Encoding) != expected.PrimaryDigest {
		return mutationRefusal(
			ctx,
			nodeID,
			"the stored node differs from the expected source",
			domain.ErrConflict,
		)
	}

	return nil
}

// checkGraphRecords reads each record in expected inside transaction, checks
// its
// primary digest, and returns the sources by node ID. It returns an error
// that wraps domain.ErrConflict if a record does not exist or its digest
// differs.
func checkGraphRecords(
	ctx context.Context,
	transaction fdb.Transaction,
	orgID uuid.UUID,
	nodeID uuid.UUID,
	expected []node.GraphRecord,
) (map[uuid.UUID]mutationSource, error) {
	sources := make(map[uuid.UUID]mutationSource, len(expected))
	for _, record := range expected {
		source, err := readMutationSource(ctx, transaction, orgID, record.NodeID)
		if err != nil {
			return nil, err
		}

		if !source.Exists || node.PrimaryDigest(source.Encoding) != record.PrimaryDigest {
			return nil, mutationRefusal(
				ctx,
				nodeID,
				"the stored record of node "+record.NodeID.String()+" differs from the graph expectation",
				domain.ErrConflict,
			)
		}

		sources[record.NodeID] = source
	}

	return sources, nil
}

// checkParentChain follows child_of from parentID through the records that
// checkGraphRecords verified. It returns an error that wraps
// domain.ErrConflict
// if an ancestor exists in transaction but does not appear in records. It
// stops at a
// node without child_of or a missing node. It returns an error that wraps
// domain.ErrInvalidArgument if the chain includes nodeID or repeats a node.
func checkParentChain(
	ctx context.Context,
	transaction fdb.Transaction,
	orgID uuid.UUID,
	nodeID uuid.UUID,
	parentID uuid.UUID,
	records map[uuid.UUID]mutationSource,
) error {
	visited := map[uuid.UUID]bool{}

	for currentID := parentID; currentID != uuid.Nil; {
		if currentID == nodeID || visited[currentID] {
			return mutationRefusal(
				ctx,
				nodeID,
				fmt.Sprintf(
					"parent %s is the node, one of its descendants, or in a child_of cycle",
					parentID,
				),
				domain.ErrInvalidArgument,
			)
		}

		visited[currentID] = true
		_, listed := records[currentID]
		if !listed {
			stored, err := readMutationSource(ctx, transaction, orgID, currentID)
			if err != nil {
				return err
			}

			if !stored.Exists && currentID != parentID {
				return nil
			}

			if !stored.Exists {
				return mutationRefusal(
					ctx,
					nodeID,
					"parent "+parentID.String()+" is not a node in the organization",
					domain.ErrInvalidArgument,
				)
			}

			return mutationRefusal(
				ctx,
				nodeID,
				"the graph expectation does not list ancestor "+currentID.String(),
				domain.ErrConflict,
			)
		}

		var err error
		currentID, err = readGraphParent(ctx, transaction, orgID, currentID)
		if err != nil {
			return err
		}
	}

	return nil
}
