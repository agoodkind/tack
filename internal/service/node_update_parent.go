package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// parentChangeForUpdate checks a parent_id entry in an update and returns the
// child_of edge changes that move the node under the new parent. The check
// runs for every caller of Update, not only the MCP tools: a parent_id that
// is not the id of a node in the same org is refused, because a node whose
// parent does not resolve is unreachable through every scoped read
// (TACK-523). An update without parent_id returns no changes.
func (s *NodeService) parentChangeForUpdate(
	ctx context.Context,
	existing *node.NodeView,
	props map[string]json.RawMessage,
	actorID uuid.UUID,
	now time.Time,
) (node.RelationshipChanges, error) {
	none := node.RelationshipChanges{Add: nil, Remove: nil}
	raw, ok := props["parent_id"]
	if !ok {
		return none, nil
	}
	if isDeletedPropValue(raw) {
		return none, fmt.Errorf("update node %s: parent_id cannot be cleared: %w", existing.ID, domain.ErrInvalidArgument)
	}
	value, ok := rawString(raw)
	if !ok {
		return none, fmt.Errorf("update node %s: parent_id must be a node id string: %w", existing.ID, domain.ErrInvalidArgument)
	}
	parentID, err := uuid.Parse(value)
	if err != nil {
		return none, fmt.Errorf("update node %s: parent_id %q is not a node id: %w", existing.ID, value, domain.ErrInvalidArgument)
	}
	resolve, err := s.reader.Resolve(ctx, parentID)
	if err != nil {
		slog.ErrorContext(ctx, "node.update.parent_resolve_failed", slog.String("err", err.Error()))
		return none, fmt.Errorf("update node %s: resolve parent %s: %w", existing.ID, parentID, err)
	}
	if resolve == nil || resolve.OrgID != existing.OrgID {
		return none, fmt.Errorf("update node %s: parent %s is not a node in the org: %w", existing.ID, parentID, domain.ErrInvalidArgument)
	}
	currentParentID := rawUUIDReferenceProperty(existing.Props, "parent_id")
	if currentParentID == parentID {
		return none, nil
	}
	changes := node.RelationshipChanges{Add: nil, Remove: nil}
	if currentParentID != uuid.Nil {
		changes.Remove = append(changes.Remove, childOfEdge(existing, currentParentID, actorID, now))
	}
	changes.Add = append(changes.Add, childOfEdge(existing, parentID, actorID, now))
	return changes, nil
}

func childOfEdge(existing *node.NodeView, targetID uuid.UUID, actorID uuid.UUID, now time.Time) *node.Relationship {
	return &node.Relationship{
		OrgID:        existing.OrgID,
		SourceID:     existing.ID,
		RelationType: node.RelChildOf,
		TargetID:     targetID,
		CreatedBy:    actorID,
		CreatedAt:    now,
		Props:        nil,
	}
}

// mergeRelationshipChanges appends the parent move onto the caller's changes.
func mergeRelationshipChanges(base node.RelationshipChanges, extra node.RelationshipChanges) node.RelationshipChanges {
	return node.RelationshipChanges{
		Add:    append(append([]*node.Relationship(nil), base.Add...), extra.Add...),
		Remove: append(append([]*node.Relationship(nil), base.Remove...), extra.Remove...),
	}
}
