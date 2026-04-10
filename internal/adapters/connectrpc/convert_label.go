// Package connectrpc converts between domain types and protobuf messages.
package connectrpc

import (
	v1 "github.com/agoodkind/tack/gen/tack/v1"
	"github.com/agoodkind/tack/internal/domain/label"
)

func protoLabel(l *label.Label) *v1.Label {
	pb := &v1.Label{
		Base:        baseFromFields(l.ID, l.CreatedAt, l.UpdatedAt, nil, nil),
		WorkspaceId: l.WorkspaceID.String(),
		Name:        l.Name,
		Color:       l.Color,
		SortOrder:   int32(l.SortOrder),
	}
	if l.ProjectID != nil {
		s := l.ProjectID.String()
		pb.ProjectId = &s
	}
	return pb
}
