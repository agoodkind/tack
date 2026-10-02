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

// ErrNoHierarchyParent reports a node with several hierarchy parents, or a
// node without a hierarchy parent when its type lists CanLiveUnder types.
var ErrNoHierarchyParent = errors.New("node requires exactly one hierarchy parent")

// children reads one bounded page of the child_of edges to resourceID and
// returns each source node that node.LivesUnder places under resourceID.
func (c *OrgScopeCompiler) children(ctx context.Context, orgID, resourceID uuid.UUID, cursor string, limit int) (node.IDPage, error) {
	kinds := make(map[string]*node.NodeType)
	resource, err := c.typeOf(ctx, orgID, resourceID, kinds)
	if err != nil {
		return node.IDPage{}, err
	}
	if resource == nil {
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
		source, err := c.typeOf(ctx, orgID, sourceID, kinds)
		if err != nil {
			return node.IDPage{}, err
		}
		if node.LivesUnder(source, resource) {
			children = append(children, sourceID)
		}
	}
	return node.IDPage{IDs: children, NextCursor: page.NextCursor, Done: page.Done}, nil
}

// typeOf returns the node type of nodeID. A node outside orgID, a missing
// node, or a missing type returns nil. kinds caches one read per type key
// for one bounded page.
func (c *OrgScopeCompiler) typeOf(ctx context.Context, orgID, nodeID uuid.UUID, kinds map[string]*node.NodeType) (*node.NodeType, error) {
	resolved, err := c.reader.Resolve(ctx, nodeID)
	if err != nil {
		return nil, hierarchyFailure(ctx, nodeID, "resolve hierarchy node "+nodeID.String(), err)
	}
	if resolved == nil || resolved.OrgID != orgID {
		return nil, nil
	}
	return c.typeByKey(ctx, orgID, resolved.NodeType, kinds)
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
