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

// errNoHierarchyParent reports a node with several hierarchy parents, or a
// node without a hierarchy parent when its type lists CanLiveUnder types.
var errNoHierarchyParent = errors.New("node requires exactly one hierarchy parent")

// livesUnder reports whether NodeType metadata places child under parent.
// The child's CanLiveUnder list or the parent's CanContain list declares
// the pair, the same rule the MCP parent resolution applies.
func livesUnder(child, parent *node.NodeType) bool {
	if child == nil || parent == nil {
		return false
	}
	return slices.Contains(child.CanLiveUnder, parent.TypeKey) || slices.Contains(parent.CanContain, child.TypeKey)
}

// parent returns the one hierarchy parent of nodeID. A hierarchy parent is
// the target node of an edge from nodeID, and livesUnder accepts the pair of
// node types. parent reads every edge from nodeID in bounded pages. Several
// edges to the same parent count once. A node without a hierarchy parent is
// a hierarchy root when its type lists no CanLiveUnder type, and parent
// returns uuid.Nil for it.
func (c *OrgScopeCompiler) parent(ctx context.Context, orgID, nodeID uuid.UUID, kind *node.NodeType) (uuid.UUID, error) {
	found := uuid.Nil
	cursor := ""
	kinds := map[string]*node.NodeType{kind.TypeKey: kind}
	for {
		page, err := c.relationships.EdgesFrom(ctx, orgID, nodeID, cursor, maxDependentPage)
		if err != nil {
			return uuid.Nil, hierarchyFailure(ctx, nodeID, "list edges from node "+nodeID.String(), err)
		}
		for _, targetID := range page.IDs {
			if targetID == found {
				continue
			}
			target, err := c.typeOf(ctx, orgID, targetID, kinds)
			if err != nil {
				return uuid.Nil, err
			}
			if !livesUnder(kind, target) {
				continue
			}
			if found != uuid.Nil {
				return uuid.Nil, hierarchyFailure(ctx, nodeID, "resolve hierarchy parent", errNoHierarchyParent)
			}
			found = targetID
		}
		if page.Done {
			break
		}
		cursor = page.NextCursor
	}
	if found == uuid.Nil && len(kind.CanLiveUnder) > 0 {
		return uuid.Nil, hierarchyFailure(ctx, nodeID, "resolve hierarchy parent", errNoHierarchyParent)
	}
	return found, nil
}

// children reads one bounded page of the edges to resourceID and returns
// each source node that livesUnder places under resourceID.
func (c *OrgScopeCompiler) children(ctx context.Context, orgID, resourceID uuid.UUID, cursor string, limit int) (node.IDPage, error) {
	kinds := make(map[string]*node.NodeType)
	resource, err := c.typeOf(ctx, orgID, resourceID, kinds)
	if err != nil {
		return node.IDPage{}, err
	}
	if resource == nil {
		return node.IDPage{IDs: []uuid.UUID{}, NextCursor: "", Done: true}, nil
	}
	page, err := c.relationships.EdgesTo(ctx, orgID, resourceID, cursor, limit)
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
		if livesUnder(source, resource) {
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
	if kind, exists := kinds[resolved.NodeType]; exists {
		return kind, nil
	}
	kind, err := c.types.TypeByKey(ctx, orgID, resolved.NodeType)
	if err != nil {
		wrapped := fmt.Errorf("read node type %q in organization %s: %w", resolved.NodeType, orgID, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.access.type_read_failed",
			slog.String("err", wrapped.Error()), slog.String("type_key", resolved.NodeType))
		return nil, loggedAccessError{err: wrapped}
	}
	kinds[resolved.NodeType] = kind
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
