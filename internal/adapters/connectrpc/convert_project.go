// Package connectrpc converts between domain types and protobuf messages.
package connectrpc

import (
	v1 "github.com/agoodkind/tack/gen/tack/v1"
	"github.com/agoodkind/tack/internal/domain/project"
)

func protoProject(p *project.Project) *v1.Project {
	pb := &v1.Project{
		Base:        baseFromFields(p.ID, p.CreatedAt, p.UpdatedAt, &p.CreatedBy, p.UpdatedBy),
		WorkspaceId: p.WorkspaceID.String(),
		Name:        p.Name,
		Identifier:  p.Identifier,
	}
	if p.Description != "" {
		pb.Description = &p.Description
	}
	if p.DefaultStateID != nil {
		s := p.DefaultStateID.String()
		pb.DefaultStateId = &s
	}
	return pb
}
