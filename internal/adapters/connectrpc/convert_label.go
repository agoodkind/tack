// Package connectrpc converts between domain types and protobuf messages.
package connectrpc

import (
	"encoding/json"

	"github.com/google/uuid"
	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/internal/domain/node"
)

func protoLabelFromView(v *node.NodeListView) *v1.Label {
	if v == nil {
		return nil
	}
	color := ""
	if v.CustomProps != nil {
		if raw, ok := v.CustomProps["color"]; ok {
			var s string
			_ = json.Unmarshal(raw, &s)
			color = s
		}
	}
	pb := &v1.Label{
		Base:        baseFromFields(v.ID, v.CreatedAt, v.UpdatedAt, &v.CreatedBy, &v.UpdatedBy),
		WorkspaceId: v.WorkspaceID.String(),
		Name:        v.Name,
		Color:       color,
		SortOrder:   int32(v.SortOrder),
	}
	if v.ProjectID != uuid.Nil {
		s := v.ProjectID.String()
		pb.ProjectId = &s
	}
	return pb
}
