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
// changes without child_of entries. One added child_of edge requests a parent.
// The checks reject missing parents, cycles, and conflicting requests.
func (s *NodeService) parentChangeForUpdate(
	ctx context.Context,
	snapshot node.MutationSnapshot,
	changes node.RelationshipChanges,
) (updateParent, node.RelationshipChanges, error) {
	none := updateParent{Change: nil, Records: nil, Relationships: nil}
	other, added, removed := splitChildOfChanges(changes)
	requestedID, err := requestedParentID(ctx, snapshot, added, removed)
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

// requestedParentID returns the parent that added and removed request,
// or [uuid.Nil] if they do not request a parent.
func requestedParentID(
	ctx context.Context,
	snapshot node.MutationSnapshot,
	added []uuid.UUID,
	removed []uuid.UUID,
) (uuid.UUID, error) {
	nodeID := snapshot.Node.ID
	requestedID := uuid.Nil

	for _, targetID := range added {
		if requestedID != uuid.Nil && requestedID != targetID {
			return uuid.Nil, parentRefusal(
				ctx,
				nodeID,
				fmt.Sprintf("the update requests parents %s and %s", requestedID, targetID),
			)
		}

		requestedID = targetID
	}

	if requestedID == uuid.Nil && len(removed) > 0 {
		requestedID = snapshot.ParentID
		if requestedID == uuid.Nil {
			return uuid.Nil, parentRefusal(
				ctx,
				nodeID,
				"removing a child_of edge requires a replacement parent",
			)
		}
	}

	for _, targetID := range removed {
		if targetID == requestedID {
			return uuid.Nil, parentRefusal(
				ctx,
				nodeID,
				"the update removes the child_of edge to its parent "+targetID.String(),
			)
		}
	}

	return requestedID, nil
}

// scopedUpdateProps returns in.Props with scope_id set to in.ScopeID. It
// returns in.Props unchanged when in.ScopeID is [uuid.Nil]. The store checks a
// new scope only against the parent chain of a new parent. A nonzero
// in.ScopeID therefore requires a requested parent that differs from
// storedParentID, and otherwise the function returns an error that wraps
// domain.ErrInvalidArgument.
func scopedUpdateProps(
	ctx context.Context,
	in UpdateInput,
	parent *node.ParentChange,
	storedParentID uuid.UUID,
) (map[string]json.RawMessage, error) {
	if in.ScopeID == uuid.Nil {
		return in.Props, nil
	}

	if parent == nil || parent.TargetID == storedParentID {
		return nil, parentRefusal(
			ctx,
			in.NodeID,
			"a scope change to "+in.ScopeID.String()+" requires a change to a different parent",
		)
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
	slog.WarnContext(
		ctx,
		"node.update.parent_refused",
		slog.String("err", err.Error()),
		slog.String("node_id", nodeID.String()),
	)

	return err
}

// splitChildOfChanges returns changes without child_of entries and the
// targets of the added and removed child_of entries.
func splitChildOfChanges(
	changes node.RelationshipChanges,
) (node.RelationshipChanges, []uuid.UUID, []uuid.UUID) {
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
