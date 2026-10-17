package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strconv"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// checkPlanShape refuses a plan that sets parent_id in Props, changes a
// child_of relationship, or specifies a different node type from the stored
// node type.
func checkPlanShape(ctx context.Context, plan node.MutationPlan, source mutationSource) error {
	if _, present := plan.Props[parentIDProperty]; present {
		return mutationRefusal(ctx, plan.NodeID, "the plan sets parent_id in Props instead of Parent", domain.ErrInvalidArgument)
	}
	for _, relationship := range append(append([]*node.Relationship{}, plan.Relationships.Add...), plan.Relationships.Remove...) {
		if relationship.RelationType == node.RelChildOf {
			wrapped := fmt.Errorf("mutate node %s: %w", plan.NodeID, node.ErrReservedParentRelation)
			slog.WarnContext(ctx, "node.mutation_refused", slog.String("err", wrapped.Error()), slog.String("node_id", plan.NodeID.String()))
			return loggedSearchError{err: wrapped}
		}
	}
	if source.Exists && source.TypeKey != plan.NodeType {
		return mutationRefusal(ctx, plan.NodeID, "the stored node type "+source.TypeKey+" differs from the plan", domain.ErrConflict)
	}
	return nil
}

// mutationResult applies the name and property patch from plan to source.
// It removes a property if its patch value is JSON null or empty.
func mutationResult(plan node.MutationPlan, source mutationSource, now time.Time) node.Node {
	result := node.Node{
		ID: plan.NodeID, OrgID: plan.OrgID, NodeType: plan.NodeType, Name: "", Props: map[string]json.RawMessage{},
		CreatedBy: plan.ActorID, UpdatedBy: plan.ActorID, CreatedAt: now, UpdatedAt: now,
	}
	if source.Exists {
		result = source.Node
		result.Props = maps.Clone(source.Node.Props)
		if result.Props == nil {
			result.Props = map[string]json.RawMessage{}
		}
		result.UpdatedBy, result.UpdatedAt = plan.ActorID, now
	}
	if plan.Name != nil {
		result.Name = *plan.Name
	}
	for name, value := range plan.Props {
		if len(value) == 0 || string(value) == "null" {
			delete(result.Props, name)
			continue
		}
		result.Props[name] = value
	}
	return result
}

// childOfChanges sets parent_id on result to the parent from plan and returns
// the child_of changes that leave exactly one edge to that parent. It returns
// an error that wraps domain.ErrConflict if a removed edge does not match a
// digest in plan.Graph.Relationships. If plan does not specify Parent, it
// preserves the stored parent and edges.
func childOfChanges(ctx context.Context, tr fdb.Transaction, plan node.MutationPlan, source mutationSource, result *node.Node) (node.RelationshipChanges, error) {
	changes := node.RelationshipChanges{Add: nil, Remove: nil}
	if plan.Parent == nil {
		return changes, nil
	}
	targetID := plan.Parent.TargetID
	result.Props[parentIDProperty] = json.RawMessage(strconv.Quote(targetID.String()))
	var edges []node.RelationshipSnapshot
	if source.Exists {
		var err error
		edges, err = readOutgoingSnapshots(ctx, tr, plan.OrgID, plan.NodeID, node.RelChildOf)
		if err != nil {
			return changes, err
		}
	}
	kept := false
	for _, edge := range edges {
		if edge.Relationship.TargetID == targetID {
			kept = true
			continue
		}
		if !expectsRelationship(plan.Graph.Relationships, edge) {
			return changes, mutationRefusal(ctx, plan.NodeID, "the child_of edge to "+edge.Relationship.TargetID.String()+" differs from the graph expectation", domain.ErrConflict)
		}
		removed := edge.Relationship
		removed.OrgID, removed.SourceID = plan.OrgID, plan.NodeID
		changes.Remove = append(changes.Remove, &removed)
	}
	if !kept {
		changes.Add = append(changes.Add, &node.Relationship{
			OrgID: plan.OrgID, SourceID: plan.NodeID, RelationType: node.RelChildOf, TargetID: targetID,
			CreatedBy: plan.ActorID, CreatedAt: result.UpdatedAt, Props: nil,
		})
	}
	return changes, nil
}

// expectsRelationship reports whether expected lists the relation type,
// target, and digest of edge.
func expectsRelationship(expected []node.RelationshipExpectation, edge node.RelationshipSnapshot) bool {
	for _, candidate := range expected {
		if candidate.RelationType == edge.Relationship.RelationType && candidate.TargetID == edge.Relationship.TargetID && candidate.Digest == edge.Digest {
			return true
		}
	}
	return false
}

// planRelationshipChanges returns the relationship changes from plan and sets
// the organization and source of each added relationship if they are empty.
func planRelationshipChanges(plan node.MutationPlan) node.RelationshipChanges {
	added := make([]*node.Relationship, 0, len(plan.Relationships.Add))
	for _, relationship := range plan.Relationships.Add {
		stamped := *relationship
		if stamped.OrgID == uuid.Nil {
			stamped.OrgID = plan.OrgID
		}
		if stamped.SourceID == uuid.Nil {
			stamped.SourceID = plan.NodeID
		}
		added = append(added, &stamped)
	}
	return node.RelationshipChanges{Add: added, Remove: plan.Relationships.Remove}
}

// mutationView returns the view record for result.
func mutationView(result *node.Node) *node.NodeView {
	return &node.NodeView{
		ID: result.ID, OrgID: result.OrgID, NodeType: result.NodeType, Name: result.Name, Props: maps.Clone(result.Props),
		CreatedBy: result.CreatedBy, UpdatedBy: result.UpdatedBy, CreatedAt: result.CreatedAt, UpdatedAt: result.UpdatedAt,
	}
}
