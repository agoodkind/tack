package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// updateParent represents the parent request for one update and the graph
// expectation that the request requires.
type updateParent struct {
	Change        *node.ParentChange
	Records       []node.GraphRecord
	Relationships []node.RelationshipExpectation
}

// parentChangeForUpdate returns the requested parent and the relationship
// changes without child_of entries. A parent_id property or one added child_of
// edge requests a parent. Removed child_of edges without another request
// preserve the stored parent_id and remove the other child_of edges. The
// checks run for every caller of Update. They return an error that wraps
// domain.ErrInvalidArgument for a parent that is not a node in the organization,
// a parent_id chain that includes the node, or conflicting requests (TACK-523).
func (s *NodeService) parentChangeForUpdate(
	ctx context.Context,
	snapshot node.MutationSnapshot,
	props map[string]json.RawMessage,
	changes node.RelationshipChanges,
) (updateParent, node.RelationshipChanges, error) {
	none := updateParent{Change: nil, Records: nil, Relationships: nil}
	other, added, removed := splitChildOfChanges(changes)
	requestedID, err := requestedParentID(ctx, snapshot, props, added, removed)
	if err != nil {
		return none, other, err
	}
	if requestedID == uuid.Nil {
		return none, other, nil
	}
	records, err := s.parentGraph(ctx, snapshot.Node.OrgID, snapshot.Node.ID, requestedID)
	if err != nil {
		return none, other, err
	}
	return updateParent{
		Change:        &node.ParentChange{TargetID: requestedID},
		Records:       records,
		Relationships: childOfExpectations(snapshot.Outgoing, requestedID),
	}, other, nil
}

// requestedParentID returns the parent that props, added, and removed request,
// or [uuid.Nil] if they do not request a parent.
func requestedParentID(ctx context.Context, snapshot node.MutationSnapshot, props map[string]json.RawMessage, added, removed []uuid.UUID) (uuid.UUID, error) {
	nodeID := snapshot.Node.ID
	requestedID := uuid.Nil
	if raw, present := props["parent_id"]; present {
		if isDeletedPropValue(raw) {
			return uuid.Nil, parentRefusal(ctx, nodeID, "parent_id cannot be cleared")
		}
		value, isText := rawString(raw)
		if !isText {
			return uuid.Nil, parentRefusal(ctx, nodeID, "parent_id must be a node id string")
		}
		parsed, err := uuid.Parse(value)
		if err != nil {
			return uuid.Nil, parentRefusal(ctx, nodeID, fmt.Sprintf("parent_id %q is not a node id", value))
		}
		requestedID = parsed
	}
	for _, targetID := range added {
		if requestedID != uuid.Nil && requestedID != targetID {
			return uuid.Nil, parentRefusal(ctx, nodeID, fmt.Sprintf("the update requests parents %s and %s", requestedID, targetID))
		}
		requestedID = targetID
	}
	if requestedID == uuid.Nil && len(removed) > 0 {
		requestedID = snapshot.ParentID
		if requestedID == uuid.Nil {
			return uuid.Nil, parentRefusal(ctx, nodeID, "removing a child_of edge requires a parent_id")
		}
	}
	for _, targetID := range removed {
		if targetID == requestedID {
			return uuid.Nil, parentRefusal(ctx, nodeID, "the update removes the child_of edge to its parent "+targetID.String())
		}
	}
	return requestedID, nil
}

// scopedUpdateProps returns in.Props with scope_id set to in.ScopeID. It
// returns in.Props unchanged when in.ScopeID is [uuid.Nil]. A scope change
// without a parent change returns an error that wraps
// domain.ErrInvalidArgument, because the store checks the new scope only
// against a new parent chain.
func scopedUpdateProps(ctx context.Context, in UpdateInput, parent *node.ParentChange) (map[string]json.RawMessage, error) {
	if in.ScopeID == uuid.Nil {
		return in.Props, nil
	}
	if parent == nil {
		return nil, parentRefusal(ctx, in.NodeID, "a scope change to "+in.ScopeID.String()+" requires a parent change")
	}
	props := make(map[string]json.RawMessage, len(in.Props)+1)
	maps.Copy(props, in.Props)
	props["scope_id"] = MustRawString(in.ScopeID.String())
	return props, nil
}

// parentRefusal logs a parent request refusal for nodeID with reason and
// returns an error that wraps domain.ErrInvalidArgument.
func parentRefusal(ctx context.Context, nodeID uuid.UUID, reason string) error {
	err := fmt.Errorf("update node %s: %s: %w", nodeID, reason, domain.ErrInvalidArgument)
	slog.WarnContext(ctx, "node.update.parent_refused", slog.String("err", err.Error()), slog.String("node_id", nodeID.String()))
	return err
}

// splitChildOfChanges returns changes without child_of entries and the
// targets of the added and removed child_of entries.
func splitChildOfChanges(changes node.RelationshipChanges) (node.RelationshipChanges, []uuid.UUID, []uuid.UUID) {
	other := node.RelationshipChanges{Add: nil, Remove: nil}
	var added, removed []uuid.UUID
	for _, relationship := range changes.Add {
		if relationship.RelationType == node.RelChildOf {
			added = append(added, relationship.TargetID)
			continue
		}
		other.Add = append(other.Add, relationship)
	}
	for _, relationship := range changes.Remove {
		if relationship.RelationType == node.RelChildOf {
			removed = append(removed, relationship.TargetID)
			continue
		}
		other.Remove = append(other.Remove, relationship)
	}
	return other, added, removed
}
