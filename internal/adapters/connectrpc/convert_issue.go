// Package connectrpc converts between domain types and protobuf messages.
package connectrpc

import (
	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/internal/domain/issue"
)

func protoIssue(i *issue.Issue) *v1.Issue {
	pb := &v1.Issue{
		Base:        baseFromDomain(i.Base),
		WorkspaceId: i.WorkspaceID.String(),
		ProjectId:   i.ProjectID.String(),
		Name:        i.Name,
		Priority:    protoPriority(i.Priority),
		AssigneeIds: uuidSlice(i.AssigneeIDs),
		LabelIds:    uuidSlice(i.LabelIDs),
		DueDate:     toTS(i.TargetDate),
	}
	if i.Description != "" {
		pb.Description = &i.Description
	}
	if i.StateID != nil {
		pb.StateId = i.StateID.String()
	}
	if i.EpicID != nil {
		s := i.EpicID.String()
		pb.EpicId = &s
	}
	if i.ParentID != nil {
		s := i.ParentID.String()
		pb.ParentId = &s
	}
	return pb
}
