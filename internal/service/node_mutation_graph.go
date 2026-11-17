package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// readMutationSnapshot reads the mutation snapshot of nodeID in orgID.
func (s *NodeService) readMutationSnapshot(
	ctx context.Context,
	orgID uuid.UUID,
	nodeID uuid.UUID,
) (node.MutationSnapshot, node.SchemaSnapshot, error) {
	snapshot, schema, err := s.nodes.ReadMutationSnapshot(ctx, orgID, nodeID)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"node.mutation_snapshot_failed",
			slog.String("err", err.Error()),
			slog.String("node_id", nodeID.String()),
		)

		return snapshot, schema, fmt.Errorf(
			"read the mutation snapshot of node %s: %w",
			nodeID,
			err,
		)
	}

	return snapshot, schema, nil
}

// parentGraph reads parentID and each ancestor along the parent_id chain and
// returns their primary digests. It stops at a node without parent_id or a
// missing ancestor. It returns an error that wraps domain.ErrInvalidArgument
// if parentID does not exist, the chain includes nodeID, or the chain
// repeats
// a node.
func (s *NodeService) parentGraph(
	ctx context.Context,
	orgID uuid.UUID,
	nodeID uuid.UUID,
	parentID uuid.UUID,
) ([]node.GraphRecord, error) {
	records := []node.GraphRecord{}
	visited := map[uuid.UUID]bool{}

	for currentID := parentID; currentID != uuid.Nil; {
		if currentID == nodeID || visited[currentID] {
			slog.WarnContext(
				ctx,
				"node.parent_cycle_refused",
				slog.String("node_id", nodeID.String()),
				slog.String("parent_id", parentID.String()),
			)

			return nil, fmt.Errorf(
				"node %s: parent %s is the node, one of its descendants, or in a parent_id cycle: %w",
				nodeID,
				parentID,
				domain.ErrInvalidArgument,
			)
		}

		visited[currentID] = true
		ancestor, _, err := s.readMutationSnapshot(ctx, orgID, currentID)
		if err != nil {
			return nil, err
		}

		if !ancestor.Exists && currentID == parentID {
			return nil, fmt.Errorf(
				"node %s: parent %s is not a node in the org: %w",
				nodeID,
				parentID,
				domain.ErrInvalidArgument,
			)
		}

		if !ancestor.Exists {
			break
		}

		records = append(
			records,
			node.GraphRecord{NodeID: currentID, PrimaryDigest: ancestor.PrimaryDigest},
		)
		currentID = ancestor.ParentID
	}

	return records, nil
}

// referenceGraph returns the primary digest of each stored node that a UUID
// property in props references if the property definition declares a
// reference
// target type.
func (s *NodeService) referenceGraph(
	ctx context.Context,
	orgID uuid.UUID,
	schema node.Schema,
	props map[string]json.RawMessage,
) ([]node.GraphRecord, error) {
	records := []node.GraphRecord{}

	for _, definition := range schema.Definitions {
		if definition.ReferenceTargetTypeKey == "" || definition.Type != node.PropertyTypeUUID ||
			definition.Name == "parent_id" {
			continue
		}

		targetID := rawUUIDReferenceProperty(props, definition.Name)
		if targetID == uuid.Nil {
			continue
		}

		target, _, err := s.readMutationSnapshot(ctx, orgID, targetID)
		if err != nil {
			return nil, err
		}

		if target.Exists {
			records = append(
				records,
				node.GraphRecord{NodeID: targetID, PrimaryDigest: target.PrimaryDigest},
			)
		}
	}

	return records, nil
}
