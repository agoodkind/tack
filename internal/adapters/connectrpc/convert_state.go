// Package connectrpc converts between domain types and protobuf messages.
package connectrpc

import (
	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/internal/domain/state"
)

func protoState(s *state.State) *v1.State {
	return &v1.State{
		Base:      baseFromFields(s.ID, s.CreatedAt, s.UpdatedAt, nil, nil),
		ProjectId: s.ProjectID.String(),
		Name:      s.Name,
		Group:     protoStateGroup(s.GroupName),
		Color:     s.Color,
		SortOrder: int32(s.SortOrder),
	}
}
