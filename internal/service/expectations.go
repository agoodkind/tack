package service

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
)

// sortedGraphRecords returns the distinct records from all lists in node ID
// order.
func sortedGraphRecords(lists ...[]node.GraphRecord) []node.GraphRecord {
	merged := []node.GraphRecord{}

	for _, list := range lists {
		for _, record := range list {
			if !slices.ContainsFunc(
				merged,
				func(existing node.GraphRecord) bool { return existing.NodeID == record.NodeID },
			) {
				merged = append(merged, record)
			}
		}
	}

	slices.SortFunc(
		merged,
		func(left, right node.GraphRecord) int {
			return strings.Compare(left.NodeID.String(), right.NodeID.String())
		},
	)

	return merged
}

// childOfExpectations returns the expected digest of each child_of edge in
// outgoing that does not target parentID.
func childOfExpectations(
	outgoing []node.RelationshipSnapshot,
	parentID uuid.UUID,
) []node.RelationshipExpectation {
	expectations := []node.RelationshipExpectation{}

	for _, edge := range outgoing {
		if edge.Relationship.RelationType == node.RelChildOf &&
			edge.Relationship.TargetID != parentID {
			expectations = append(
				expectations,
				node.RelationshipExpectation{
					RelationType: node.RelChildOf,
					TargetID:     edge.Relationship.TargetID,
					Digest:       edge.Digest,
				},
			)
		}
	}

	return expectations
}

// createRelationships returns the relationships from input with the new node
// as
// the default source. It returns an error that wraps
// node.ErrReservedParentRelation for a child_of relationship. ParentID sets
// the parent.
func createRelationships(
	ctx context.Context,
	nodeID uuid.UUID,
	input CreateInput,
) ([]*node.Relationship, error) {
	now := clock.Now().UTC()

	relationships := make([]*node.Relationship, 0, len(input.Relationships))
	for _, relationship := range input.Relationships {
		if relationship.RelationType == node.RelChildOf {
			err := fmt.Errorf("create node %s: %w", nodeID, node.ErrReservedParentRelation)
			slog.WarnContext(ctx, "node.create_refused", slog.String("err", err.Error()))

			return nil, err
		}

		if relationship.SourceID == uuid.Nil {
			relationship.SourceID = nodeID
		}

		if relationship.CreatedAt.IsZero() {
			relationship.CreatedAt = now
		}

		if relationship.CreatedBy == uuid.Nil {
			relationship.CreatedBy = input.ActorID
		}

		relationships = append(relationships, relationship)
	}

	return relationships, nil
}
