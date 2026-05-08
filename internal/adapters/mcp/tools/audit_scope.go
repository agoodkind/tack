package tools

import (
	"context"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func stampAuditOrg(ctx context.Context, orgID uuid.UUID) {
	if orgID == uuid.Nil {
		return
	}
	audit.SetScopeFields(ctx, audit.Scope{OrgID: orgID})
}

func stampAuditEntryPoint(ctx context.Context, view *node.NodeView) {
	if view == nil {
		return
	}
	audit.SetScopeFields(ctx, audit.Scope{
		OrgID:       view.OrgID,
		WorkspaceID: view.ID,
	})
}

func stampAuditNodeView(ctx context.Context, view *node.NodeView) {
	if view == nil {
		return
	}
	stampAuditOrg(ctx, view.OrgID)
}

func stampAuditNodeResolve(ctx context.Context, resolve *node.NodeResolve) {
	if resolve == nil {
		return
	}
	stampAuditOrg(ctx, resolve.OrgID)
}

func auditEntryPointArg(ctx context.Context, args argMap, b NodeTypeBinding) (*node.NodeView, error) {
	if b.Resolver == nil {
		return nil, nil
	}
	entryPointReference := b.Resolver.optionalEntryPointReference(args)
	if entryPointReference == "" {
		return nil, nil
	}
	return b.Resolver.Workspace(ctx, entryPointReference)
}

func stampAuditNodeInEntryPoint(ctx context.Context, resolver *Resolver, entryPoint *node.NodeView, view *node.NodeView) error {
	if view == nil {
		return nil
	}
	if entryPoint != nil {
		if view.OrgID != entryPoint.OrgID || !viewBelongsToScope(ctx, resolver, view, entryPoint.ID) {
			return domain.ErrNotFound
		}
		stampAuditEntryPoint(ctx, entryPoint)
	}
	stampAuditNodeView(ctx, view)
	return nil
}
