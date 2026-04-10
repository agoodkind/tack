// Package connectrpc converts between domain types and protobuf messages.
package connectrpc

import (
	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/internal/domain/module"
)

func protoModule(m *module.Module) *v1.Module {
	pb := &v1.Module{
		Base:        baseFromFields(m.ID, m.CreatedAt, m.UpdatedAt, &m.CreatedBy, m.UpdatedBy),
		WorkspaceId: m.WorkspaceID.String(),
		ProjectId:   m.ProjectID.String(),
		Name:        m.Name,
		StartDate:   toTS(m.StartDate),
		TargetDate:  toTS(m.TargetDate),
	}
	if m.Description != "" {
		pb.Description = &m.Description
	}
	if m.Status != "" {
		pb.Status = &m.Status
	}
	return pb
}
