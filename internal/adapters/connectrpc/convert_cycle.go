// Package connectrpc converts between domain types and protobuf messages.
package connectrpc

import (
	v1 "github.com/agoodkind/tack/gen/tack/v1"
	"github.com/agoodkind/tack/internal/domain/cycle"
)

func protoCycle(c *cycle.Cycle) *v1.Cycle {
	pb := &v1.Cycle{
		Base:        baseFromFields(c.ID, c.CreatedAt, c.UpdatedAt, &c.CreatedBy, c.UpdatedBy),
		WorkspaceId: c.WorkspaceID.String(),
		ProjectId:   c.ProjectID.String(),
		Name:        c.Name,
		StartDate:   toTS(c.StartDate),
		EndDate:     toTS(c.EndDate),
	}
	if c.Description != "" {
		pb.Description = &c.Description
	}
	return pb
}
