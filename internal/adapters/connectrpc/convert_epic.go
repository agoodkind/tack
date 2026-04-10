// Package connectrpc converts between domain types and protobuf messages.
package connectrpc

import (
	v1 "github.com/agoodkind/tack/gen/tack/v1"
	"github.com/agoodkind/tack/internal/domain/epic"
)

func protoEpic(e *epic.Epic) *v1.Epic {
	pb := &v1.Epic{
		Base:        baseFromDomain(e.Base),
		WorkspaceId: e.WorkspaceID.String(),
		ProjectId:   e.ProjectID.String(),
		Name:        e.Name,
		Priority:    protoPriority(e.Priority),
		AssigneeIds: uuidSlice(e.AssigneeIDs),
		LabelIds:    uuidSlice(e.LabelIDs),
	}
	if e.Description != "" {
		pb.Description = &e.Description
	}
	if e.StateID != nil {
		s := e.StateID.String()
		pb.StateId = &s
	}
	if e.ParentID != nil {
		s := e.ParentID.String()
		pb.ParentId = &s
	}
	return pb
}
