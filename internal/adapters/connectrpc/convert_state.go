// Package connectrpc converts between domain types and protobuf messages.
package connectrpc

import (
	"encoding/json"

	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/state"
)

func protoStateFromView(v *node.NodeListView) *v1.State {
	if v == nil {
		return nil
	}
	groupName := state.GroupBacklog
	color := ""
	if v.CustomProps != nil {
		if raw, ok := v.CustomProps["group_name"]; ok {
			var s string
			_ = json.Unmarshal(raw, &s)
			groupName = state.GroupName(s)
		}
		if raw, ok := v.CustomProps["color"]; ok {
			var s string
			_ = json.Unmarshal(raw, &s)
			color = s
		}
	}
	return &v1.State{
		Base:      baseFromFields(v.ID, v.CreatedAt, v.UpdatedAt, &v.CreatedBy, &v.UpdatedBy),
		ProjectId: v.ProjectID.String(),
		Name:      v.Name,
		Group:     protoStateGroup(groupName),
		Color:     color,
		SortOrder: int32(v.SortOrder),
	}
}
