package tools

import (
	"context"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// livesUnderEntryPoint reports whether nt declares an entry point type as
// its first CanLiveUnder parent. ScopeChainForType returns an empty chain
// for that type.
func (r *Resolver) livesUnderEntryPoint(nt *node.NodeType) bool {
	if len(nt.CanLiveUnder) == 0 {
		return false
	}
	parent := r.typeIndex[nt.CanLiveUnder[0]]
	return parent != nil && parent.Features.Has(node.FeatureIsEntryPoint)
}

// resolveEntryPointScopedReference resolves a reference such as "acme::bug"
// for a type nt directly under the entry point. scopeIdent identifies the
// entry point, and localRef supplies the reference property value among its
// children.
func (r *Resolver) resolveEntryPointScopedReference(
	ctx context.Context,
	nt *node.NodeType,
	propName string,
	scopeIdent string,
	localRef string,
) (uuid.UUID, error) {
	entryPoint, err := r.Workspace(ctx, scopeIdent)
	if err != nil {
		return uuid.Nil, err
	}
	return r.resolveNodeWithinScope(ctx, entryPoint.OrgID, entryPoint.ID, nt.TypeKey, propName, localRef)
}
