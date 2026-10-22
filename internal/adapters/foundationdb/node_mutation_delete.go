package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// DeleteNode deletes exactly one node and its relationships in one transaction.
// Each attempt rereads the source, expected graph records, and relationships
// from and to the node. It returns an error that wraps
// domain.ErrFailedPrecondition if the node has a hierarchy child or more than
// MutationEdgeLimit relationships in one direction. It returns an error that
// wraps domain.ErrConflict if a relationship differs from the graph expectation.
func (s *NodeStore) DeleteNode(ctx context.Context, plan node.DeletionPlan) (receipt node.MutationReceipt, err error) {
	defer telemetry.FDBOp(ctx, "store.node.delete_node")(&err)
	changes, err := beginMutationAudit(ctx, plan.OrgID)
	if err != nil {
		return node.MutationReceipt{}, err
	}
	err = runNodeMutation(ctx, s.db, "delete node "+plan.NodeID.String(), func(tr fdb.Transaction) error {
		changes.startAttempt()
		var attemptErr error
		receipt, attemptErr = s.deleteNodeAttempt(ctx, tr, plan, changes)
		if attemptErr != nil {
			return attemptErr
		}
		return changes.append(ctx, tr)
	})
	if err != nil {
		return node.MutationReceipt{}, err
	}
	changes.finish(ctx)
	return receipt, nil
}

// deleteNodeAttempt runs one attempt of plan inside tr and records node and
// relationship deletions in changes.
func (s *NodeStore) deleteNodeAttempt(ctx context.Context, tr fdb.Transaction, plan node.DeletionPlan, changes *mutationAudit) (node.MutationReceipt, error) {
	none := node.MutationReceipt{OrgID: plan.OrgID, NodeID: plan.NodeID, SourceDigest: node.Digest{}, ResultDigest: node.Digest{}}
	if plan.Source.Absent {
		return none, mutationRefusal(ctx, plan.NodeID, "a deletion requires an expected stored node", domain.ErrInvalidArgument)
	}
	source, err := readMutationSource(ctx, tr, plan.OrgID, plan.NodeID)
	if err != nil {
		return none, err
	}
	if err := checkSource(ctx, plan.NodeID, plan.Source, source); err != nil {
		return none, err
	}
	if _, err := checkGraphRecords(ctx, tr, plan.OrgID, plan.NodeID, plan.Graph.Records); err != nil {
		return none, err
	}
	outgoing, err := readOutgoingSnapshots(ctx, tr, plan.OrgID, plan.NodeID, "")
	if err != nil {
		return none, err
	}
	incoming, overflow, err := readIncomingSnapshots(ctx, tr, plan.OrgID, plan.NodeID)
	if err != nil {
		return none, err
	}
	if overflow || len(outgoing) > node.MutationEdgeLimit {
		return none, mutationRefusal(ctx, plan.NodeID, "the node has more relationships than one transaction deletes", domain.ErrFailedPrecondition)
	}
	if err := refuseHierarchyChild(ctx, tr, plan, incoming); err != nil {
		return none, err
	}
	if err := refuseReferencedNode(ctx, tr, plan.OrgID, source); err != nil {
		return none, err
	}
	if !outgoingMatches(plan.Graph.Relationships, outgoing) || !incomingMatches(plan.Graph.Incoming, incoming) {
		return none, mutationRefusal(ctx, plan.NodeID, "the stored relationships differ from the graph expectation", domain.ErrConflict)
	}
	if _, err := s.fenceAttempt(ctx, tr, plan.OrgID, plan.NodeID); err != nil {
		return none, err
	}
	if err := s.clearNode(ctx, tr, &source.Node, changes); err != nil {
		return none, err
	}
	none.SourceDigest = node.PrimaryDigest(source.Encoding)
	return none, nil
}

// refuseHierarchyChild returns an error that wraps domain.ErrFailedPrecondition
// if the source of a child_of edge in incoming has the node to delete as its
// hierarchy parent.
func refuseHierarchyChild(ctx context.Context, tr fdb.Transaction, plan node.DeletionPlan, incoming []node.RelationshipSnapshot) error {
	sourceIDs := make([]uuid.UUID, 0, len(incoming))
	for _, edge := range incoming {
		if edge.Relationship.RelationType == node.RelChildOf {
			sourceIDs = append(sourceIDs, edge.Relationship.SourceID)
		}
	}
	sources, err := readHierarchyNodes(ctx, tr, plan.OrgID, sourceIDs, make(map[string]*node.NodeType))
	if err != nil {
		return err
	}
	for _, source := range sources {
		if !source.Found {
			continue
		}
		isChild, err := hasHierarchyParent(ctx, tr, plan.OrgID, source, plan.NodeID)
		if err != nil {
			return err
		}
		if isChild {
			return mutationRefusal(ctx, plan.NodeID, "node "+source.ID.String()+" is a hierarchy child of the node", domain.ErrFailedPrecondition)
		}
	}
	return nil
}

// outgoingMatches reports whether expected lists exactly the relation type,
// target, and digest of each edge in stored.
func outgoingMatches(expected []node.RelationshipExpectation, stored []node.RelationshipSnapshot) bool {
	if len(expected) != len(stored) {
		return false
	}
	for _, edge := range stored {
		if !expectsRelationship(expected, edge) {
			return false
		}
	}
	return true
}

// incomingMatches reports whether expected lists exactly the source, relation
// type, and digest of each edge in stored.
func incomingMatches(expected []node.IncomingRelationshipExpectation, stored []node.RelationshipSnapshot) bool {
	if len(expected) != len(stored) {
		return false
	}
	for _, edge := range stored {
		matched := false
		for _, candidate := range expected {
			if candidate.SourceID == edge.Relationship.SourceID && candidate.RelationType == edge.Relationship.RelationType && candidate.Digest == edge.Digest {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
