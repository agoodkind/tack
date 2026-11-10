package tools

import (
	"context"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// livesUnderEntryPoint reports whether the first parent type that nt declares
// in CanLiveUnder is the entry point type. ScopeChainForType returns an empty
// chain for such a type.
func (r *Resolver) livesUnderEntryPoint(nt *node.NodeType) bool {
	if len(nt.CanLiveUnder) == 0 {
		return false
	}
	parent := r.typeIndex[nt.CanLiveUnder[0]]
	return parent != nil && parent.Features.Has(node.FeatureIsEntryPoint)
}

// resolveEntryPointScopedReference resolves a scoped reference such as
// "acme::bug" when nodes of type nt live directly under the entry point.
// scopeIdent is the entry point reference, and localRef is the value of the
// reference property among the children of that entry point.
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
