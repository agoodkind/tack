package searchaccess

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// entryPoint returns the entry point of resourceID under the shared
// [EntryPoints] rule. Query compiles caller keys only for entry-point types,
// and no caller key matches the key of a root. Each read uses the compiler's
// node, type, and relationship readers.
func (c *OrgScopeCompiler) entryPoint(ctx context.Context, orgID, resourceID uuid.UUID) (uuid.UUID, error) {
	if c.reader == nil || c.types == nil || c.relationships == nil {
		return uuid.Nil, entryPointFailure(ctx, resourceID, "resolve entry point", fmt.Errorf("search policy dependencies are unavailable"))
	}
	resource := OrgNode{OrgID: orgID, NodeID: resourceID}
	entries, defects, err := EntryPoints(ctx, compilerHierarchy{compiler: c}, []OrgNode{resource})
	if err != nil {
		return uuid.Nil, WithContext("resolve entry point for resource "+resourceID.String(), err)
	}
	if defect := defects[resource]; defect != nil {
		return uuid.Nil, entryPointFailure(ctx, resourceID, "resolve entry point", defect)
	}
	return entries[resource], nil
}

// compilerHierarchy reads hierarchy state through the compiler's readers.
// Each read is a separate FoundationDB transaction.
type compilerHierarchy struct {
	compiler *OrgScopeCompiler
}

// Ancestors reads each node's view, its node type, and, below an entry
// point, its child_of targets in bounded pages.
func (h compilerHierarchy) Ancestors(ctx context.Context, nodes []OrgNode) (map[OrgNode]AncestorState, error) {
	states := make(map[OrgNode]AncestorState, len(nodes))
	kinds := map[string]*node.NodeType{}
	for _, key := range nodes {
		view, err := h.compiler.reader.Get(ctx, key.NodeID)
		if err != nil {
			return nil, hierarchyFailure(ctx, key.NodeID, "get search ancestor "+key.NodeID.String(), err)
		}
		if view == nil {
			states[key] = AncestorState{Exists: false, OrgID: uuid.Nil, TypeKey: "", Type: nil, ChildOfs: nil}
			continue
		}
		if view.OrgID != key.OrgID {
			states[key] = AncestorState{Exists: true, OrgID: view.OrgID, TypeKey: "", Type: nil, ChildOfs: nil}
			continue
		}
		kind, err := h.compiler.typeByKey(ctx, key.OrgID, view.NodeType, kinds)
		if err != nil {
			return nil, err
		}
		state := AncestorState{Exists: true, OrgID: view.OrgID, TypeKey: view.NodeType, Type: kind, ChildOfs: nil}
		if kind != nil && !kind.Features.Has(node.FeatureIsEntryPoint) {
			state.ChildOfs, err = h.compiler.childOfs(ctx, key)
			if err != nil {
				return nil, err
			}
		}
		states[key] = state
	}
	return states, nil
}

// Targets reads each node's resolve record and, inside the requested
// organization, its node type.
func (h compilerHierarchy) Targets(ctx context.Context, nodes []OrgNode) (map[OrgNode]TargetState, error) {
	states := make(map[OrgNode]TargetState, len(nodes))
	kinds := map[string]*node.NodeType{}
	for _, key := range nodes {
		resolved, err := h.compiler.reader.Resolve(ctx, key.NodeID)
		if err != nil {
			return nil, hierarchyFailure(ctx, key.NodeID, "resolve hierarchy node "+key.NodeID.String(), err)
		}
		if resolved == nil {
			states[key] = TargetState{Exists: false, OrgID: uuid.Nil, Type: nil}
			continue
		}
		if resolved.OrgID != key.OrgID {
			states[key] = TargetState{Exists: true, OrgID: resolved.OrgID, Type: nil}
			continue
		}
		kind, err := h.compiler.typeByKey(ctx, key.OrgID, resolved.NodeType, kinds)
		if err != nil {
			return nil, err
		}
		states[key] = TargetState{Exists: true, OrgID: resolved.OrgID, Type: kind}
	}
	return states, nil
}

// childOfs reads every child_of target of one node in bounded pages.
func (c *OrgScopeCompiler) childOfs(ctx context.Context, key OrgNode) ([]uuid.UUID, error) {
	targets := []uuid.UUID{}
	cursor := ""
	for {
		page, err := c.relationships.EdgesFrom(ctx, key.OrgID, key.NodeID, node.RelChildOf, cursor, maxDependentPage)
		if err != nil {
			return nil, hierarchyFailure(ctx, key.NodeID, "list edges from node "+key.NodeID.String(), err)
		}
		targets = append(targets, page.IDs...)
		if page.Done {
			return targets, nil
		}
		cursor = page.NextCursor
	}
}

func entryPointFailure(ctx context.Context, resourceID uuid.UUID, operation string, err error) error {
	wrapped := fmt.Errorf("%s for resource %s: %w", operation, resourceID, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.access.entry_point_failed",
		slog.String("err", wrapped.Error()), slog.String("resource_id", resourceID.String()))
	return loggedAccessError{err: wrapped}
}
