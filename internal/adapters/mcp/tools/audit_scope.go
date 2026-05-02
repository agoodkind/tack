package tools

import (
	"context"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
)

func stampAuditOrg(ctx context.Context, orgID uuid.UUID) {
	if orgID == uuid.Nil {
		return
	}
	audit.SetScopeFields(ctx, audit.Scope{OrgID: orgID})
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
