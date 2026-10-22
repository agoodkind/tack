package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// deletesAlone reports whether snapshot represents a node that DeleteNode can
// delete in one transaction. The node must not have an incoming child_of edge
// or more than node.MutationEdgeLimit relationships in either direction.
func deletesAlone(snapshot node.MutationSnapshot) bool {
	if snapshot.IncomingOverflow || len(snapshot.Outgoing) > node.MutationEdgeLimit {
		return false
	}
	for _, edge := range snapshot.Incoming {
		if edge.Relationship.RelationType == node.RelChildOf {
			return false
		}
	}
	return true
}

// deleteNode deletes the node in snapshot through DeleteNode, using the
// relationships from snapshot as the graph expectation.
func (s *NodeService) deleteNode(ctx context.Context, snapshot node.MutationSnapshot, actorID uuid.UUID) (DeleteResult, error) {
	none := DeleteResult{JobID: uuid.Nil, Deleted: 0, Moved: 0, State: node.SubtreeDeleteRunning}
	outgoing := make([]node.RelationshipExpectation, 0, len(snapshot.Outgoing))
	for _, edge := range snapshot.Outgoing {
		outgoing = append(outgoing, node.RelationshipExpectation{
			RelationType: edge.Relationship.RelationType, TargetID: edge.Relationship.TargetID, Digest: edge.Digest,
		})
	}
	incoming := make([]node.IncomingRelationshipExpectation, 0, len(snapshot.Incoming))
	for _, edge := range snapshot.Incoming {
		incoming = append(incoming, node.IncomingRelationshipExpectation{
			SourceID: edge.Relationship.SourceID, RelationType: edge.Relationship.RelationType, Digest: edge.Digest,
		})
	}
	plan := node.DeletionPlan{
		OrgID: snapshot.Node.OrgID, NodeID: snapshot.Node.ID, ActorID: actorID,
		Source: node.SourceExpectation{Absent: false, PrimaryDigest: snapshot.PrimaryDigest},
		Graph:  node.GraphExpectation{Records: nil, Relationships: outgoing, Incoming: incoming},
	}
	if _, err := s.nodes.DeleteNode(ctx, plan); err != nil {
		slog.ErrorContext(ctx, "node.delete_failed", slog.String("err", err.Error()), slog.String("node_id", snapshot.Node.ID.String()))
		return none, fmt.Errorf("delete node %s: %w", snapshot.Node.ID, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "node.deleted", slog.String("node_id", snapshot.Node.ID.String()), slog.Int("deleted", 1))
	return DeleteResult{JobID: uuid.Nil, Deleted: 1, Moved: 0, State: node.SubtreeDeleteFinished}, nil
}
