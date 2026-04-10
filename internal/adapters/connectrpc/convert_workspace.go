// Package connectrpc converts between domain types and protobuf messages.
package connectrpc

import (
	v1 "github.com/agoodkind/tack/gen/tack/v1"
	"github.com/agoodkind/tack/internal/domain/workspace"
)

func protoWorkspace(w *workspace.Workspace) *v1.Workspace {
	return &v1.Workspace{
		Base:  baseFromFields(w.ID, w.CreatedAt, w.UpdatedAt, nil, nil),
		OrgId: w.OrgID.String(),
		Name:  w.Name,
		Slug:  w.Slug,
	}
}
