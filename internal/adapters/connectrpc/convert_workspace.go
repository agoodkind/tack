// Package connectrpc converts between domain types and protobuf messages.
package connectrpc

import (
	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/internal/domain/workspace"
)

func protoWorkspace(w *workspace.Workspace) *v1.Workspace {
	return &v1.Workspace{
		Base:  baseFromFields(w.ID, w.CreatedAt, w.UpdatedAt, nil, nil),
		OrgId: w.OrgID.String(),
		Name:  w.Name,
		Slug:  w.Slug,
	}
}
