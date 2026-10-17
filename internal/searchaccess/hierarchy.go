package searchaccess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// children reads one bounded page of the child_of edges to resourceID and
// returns each source node of orgID. Under [node.HierarchyParent], the
// target of a node's child_of edge is its parent whatever the two types
// are. A source with child_of edges to several targets is a defect, and it
// stays a dependent: its next access compilation withdraws its keys.
func (c *OrgScopeCompiler) children(ctx context.Context, orgID, resourceID uuid.UUID, cursor string, limit int) (node.IDPage, error) {
	exists, err := c.inOrg(ctx, orgID, resourceID)
	if err != nil {
		return node.IDPage{}, err
	}
	if !exists {
		return node.IDPage{IDs: []uuid.UUID{}, NextCursor: "", Done: true}, nil
	}
	page, err := c.relationships.EdgesTo(ctx, orgID, resourceID, node.RelChildOf, cursor, limit)
	if err != nil {
		return node.IDPage{}, hierarchyFailure(ctx, resourceID, "list edges to node "+resourceID.String(), err)
	}
	children := make([]uuid.UUID, 0, len(page.IDs))
	for _, sourceID := range page.IDs {
		if slices.Contains(children, sourceID) {
			continue
		}
		sourceExists, err := c.inOrg(ctx, orgID, sourceID)
		if err != nil {
			return node.IDPage{}, err
		}
		if sourceExists {
			children = append(children, sourceID)
		}
	}
	return node.IDPage{IDs: children, NextCursor: page.NextCursor, Done: page.Done}, nil
}

// inOrg reports whether nodeID resolves to a node of orgID.
func (c *OrgScopeCompiler) inOrg(ctx context.Context, orgID, nodeID uuid.UUID) (bool, error) {
	resolved, err := c.reader.Resolve(ctx, nodeID)
	if err != nil {
		return false, hierarchyFailure(ctx, nodeID, "resolve hierarchy node "+nodeID.String(), err)
	}
	return resolved != nil && resolved.OrgID == orgID, nil
}

// typeByKey returns the node type of orgID that uses typeKey, or nil when
// none does. kinds caches one read per type key.
func (c *OrgScopeCompiler) typeByKey(ctx context.Context, orgID uuid.UUID, typeKey string, kinds map[string]*node.NodeType) (*node.NodeType, error) {
	if kind, exists := kinds[typeKey]; exists {
		return kind, nil
	}
	kind, err := c.types.TypeByKey(ctx, orgID, typeKey)
	if err != nil {
		wrapped := fmt.Errorf("read node type %q in organization %s: %w", typeKey, orgID, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.access.type_read_failed",
			slog.String("err", wrapped.Error()), slog.String("type_key", typeKey))
		return nil, loggedAccessError{err: wrapped}
	}
	kinds[typeKey] = kind
	return kind, nil
}

func hierarchyFailure(ctx context.Context, nodeID uuid.UUID, operation string, err error) error {
	var logged loggedAccessError
	if errors.As(err, &logged) {
		return WithContext(operation, err)
	}
	wrapped := fmt.Errorf("%s for node %s: %w", operation, nodeID, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.access.hierarchy_failed",
		slog.String("err", wrapped.Error()), slog.String("node_id", nodeID.String()))
	return loggedAccessError{err: wrapped}
}
