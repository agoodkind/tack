// Package connectrpc converts between domain types and protobuf messages.
package connectrpc

import (
	"encoding/json"

	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/internal/domain/node"
)

func protoWorkspaceFromView(view *node.NodeListView) *v1.Workspace {
	// Extract slug from CustomProps
	slug := ""
	if slugBytes, ok := view.CustomProps["slug"]; ok {
		_ = json.Unmarshal(slugBytes, &slug)
	}

	return &v1.Workspace{
		Base:  baseFromFields(view.ID, view.CreatedAt, view.UpdatedAt, nil, nil),
		OrgId: view.OrgID.String(),
		Name:  view.Name,
		Slug:  slug,
	}
}
