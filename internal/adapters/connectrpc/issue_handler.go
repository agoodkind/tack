package connectrpc

import (
	"context"

	"connectrpc.com/connect"
	v1 "github.com/agoodkind/tack/gen/tack/v1"
	"github.com/agoodkind/tack/gen/tack/v1/tackv1connect"
	"github.com/agoodkind/tack/internal/auth"
	"github.com/agoodkind/tack/internal/domain/issue"
	"github.com/agoodkind/tack/internal/domain/node"
	issuesvc "github.com/agoodkind/tack/internal/service"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"
)

type IssueHandler struct {
	issueSvc    issue.Service
	assignments node.AssignmentRepository
}

var _ tackv1connect.IssueServiceHandler = (*IssueHandler)(nil)

func NewIssueHandler(issueSvc issue.Service, assignments node.AssignmentRepository) *IssueHandler {
	return &IssueHandler{issueSvc: issueSvc, assignments: assignments}
}

func (h *IssueHandler) CreateIssue(ctx context.Context, req *connect.Request[v1.CreateIssueRequest]) (*connect.Response[v1.Issue], error) {
	userID := auth.MustUserID(ctx)
	msg := req.Msg
	i := &issue.Issue{
		WorkspaceID: mustUUID(msg.WorkspaceId),
		ProjectID:   mustUUID(msg.ProjectId),
		Name:        msg.Name,
		AssigneeIDs: parseUUIDs(msg.AssigneeIds),
		LabelIDs:    parseUUIDs(msg.LabelIds),
	}
	i.CreatedBy = userID
	if msg.Description != nil {
		i.Description = *msg.Description
	}
	if msg.Priority != nil {
		i.Priority = domainPriority(*msg.Priority)
	}
	if msg.StateId != nil {
		id := mustUUID(*msg.StateId)
		i.StateID = &id
	}
	if msg.EpicId != nil {
		id := mustUUID(*msg.EpicId)
		i.EpicID = &id
	}
	if msg.ParentId != nil {
		id := mustUUID(*msg.ParentId)
		i.ParentID = &id
	}
	i.TargetDate = optTS(msg.DueDate)

	created, err := h.issueSvc.Create(ctx, i)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoIssue(created)), nil
}

func (h *IssueHandler) GetIssue(ctx context.Context, req *connect.Request[v1.GetIssueRequest]) (*connect.Response[v1.Issue], error) {
	msg := req.Msg
	i, err := h.issueSvc.Get(ctx, mustUUID(msg.WorkspaceId), mustUUID(msg.ProjectId), mustUUID(msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoIssue(i)), nil
}

func (h *IssueHandler) ListIssues(ctx context.Context, req *connect.Request[v1.ListIssuesRequest]) (*connect.Response[v1.ListIssuesResponse], error) {
	msg := req.Msg
	filter := issue.ListFilter{
		WorkspaceID: mustUUID(msg.WorkspaceId),
		PerPage:     50,
	}
	if msg.ProjectId != nil {
		id := mustUUID(*msg.ProjectId)
		filter.ProjectID = &id
	}
	if msg.EpicId != nil {
		id := mustUUID(*msg.EpicId)
		filter.EpicID = &id
	}
	if msg.StateId != nil {
		filter.StateIDs = []uuid.UUID{mustUUID(*msg.StateId)}
	}
	if msg.Priority != nil {
		filter.Priorities = []issue.Priority{domainPriority(*msg.Priority)}
	}
	if msg.AssigneeId != nil {
		filter.AssigneeIDs = []uuid.UUID{mustUUID(*msg.AssigneeId)}
	}
	if msg.PageSize != nil && *msg.PageSize > 0 {
		filter.PerPage = int(*msg.PageSize)
	}

	issues, total, err := h.issueSvc.List(ctx, filter)
	if err != nil {
		return nil, domainErr(err)
	}
	items := make([]*v1.Issue, len(issues))
	for i, iss := range issues {
		items[i] = protoIssue(iss)
	}
	return connect.NewResponse(&v1.ListIssuesResponse{
		Issues: items,
		Total:  int32(total),
	}), nil
}

func (h *IssueHandler) UpdateIssue(ctx context.Context, req *connect.Request[v1.UpdateIssueRequest]) (*connect.Response[v1.Issue], error) {
	msg := req.Msg
	i, err := h.issueSvc.Get(ctx, mustUUID(msg.WorkspaceId), mustUUID(msg.ProjectId), mustUUID(msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	if msg.Name != nil {
		i.Name = *msg.Name
	}
	if msg.Description != nil {
		i.Description = *msg.Description
	}
	if msg.Priority != nil {
		i.Priority = domainPriority(*msg.Priority)
	}
	if msg.StateId != nil {
		id := mustUUID(*msg.StateId)
		i.StateID = &id
	}
	if msg.EpicId != nil {
		id := mustUUID(*msg.EpicId)
		i.EpicID = &id
	}
	if msg.ParentId != nil {
		id := mustUUID(*msg.ParentId)
		i.ParentID = &id
	}
	if msg.DueDate != nil {
		i.TargetDate = optTS(msg.DueDate)
	}
	updated, err := h.issueSvc.Update(ctx, i)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoIssue(updated)), nil
}

func (h *IssueHandler) DeleteIssue(ctx context.Context, req *connect.Request[v1.DeleteIssueRequest]) (*connect.Response[emptypb.Empty], error) {
	msg := req.Msg
	err := h.issueSvc.Delete(ctx, mustUUID(msg.WorkspaceId), mustUUID(msg.ProjectId), mustUUID(msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *IssueHandler) AssignIssue(ctx context.Context, req *connect.Request[v1.AssignIssueRequest]) (*connect.Response[v1.Issue], error) {
	userID := auth.MustUserID(ctx)
	msg := req.Msg
	i, err := h.issueSvc.Get(ctx, mustUUID(msg.WorkspaceId), mustUUID(msg.ProjectId), mustUUID(msg.IssueId))
	if err != nil {
		return nil, domainErr(err)
	}
	newUser := mustUUID(msg.UserId)
	// Add to existing assignees via FDB
	existing, _ := h.assignments.ListByNode(ctx, i.WorkspaceID, i.NodeID)
	updated := make([]uuid.UUID, len(existing))
	copy(updated, existing)
	for _, id := range existing {
		if id == newUser {
			return connect.NewResponse(protoIssue(i)), nil // already assigned
		}
	}
	updated = append(updated, newUser)
	if err := h.assignments.SetAll(ctx, i.WorkspaceID, i.NodeID, updated, userID); err != nil {
		return nil, domainErr(err)
	}
	i.AssigneeIDs = updated
	return connect.NewResponse(protoIssue(i)), nil
}

func (h *IssueHandler) UnassignIssue(ctx context.Context, req *connect.Request[v1.UnassignIssueRequest]) (*connect.Response[v1.Issue], error) {
	userID := auth.MustUserID(ctx)
	msg := req.Msg
	i, err := h.issueSvc.Get(ctx, mustUUID(msg.WorkspaceId), mustUUID(msg.ProjectId), mustUUID(msg.IssueId))
	if err != nil {
		return nil, domainErr(err)
	}
	remove := mustUUID(msg.UserId)
	existing, _ := h.assignments.ListByNode(ctx, i.WorkspaceID, i.NodeID)
	filtered := make([]uuid.UUID, 0, len(existing))
	for _, id := range existing {
		if id != remove {
			filtered = append(filtered, id)
		}
	}
	if err := h.assignments.SetAll(ctx, i.WorkspaceID, i.NodeID, filtered, userID); err != nil {
		return nil, domainErr(err)
	}
	i.AssigneeIDs = filtered
	return connect.NewResponse(protoIssue(i)), nil
}

func (h *IssueHandler) SetIssueEpic(ctx context.Context, req *connect.Request[v1.SetIssueEpicRequest]) (*connect.Response[v1.Issue], error) {
	msg := req.Msg
	issueID := mustUUID(msg.IssueId)
	epicID := optUUID(msg.EpicId)
	if err := h.issueSvc.SetEpic(ctx, issueID, epicID); err != nil {
		return nil, domainErr(err)
	}
	i, err := h.issueSvc.Get(ctx, mustUUID(msg.WorkspaceId), mustUUID(msg.ProjectId), issueID)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoIssue(i)), nil
}

func (h *IssueHandler) SearchIssues(ctx context.Context, req *connect.Request[v1.SearchIssuesRequest]) (*connect.Response[v1.SearchIssuesResponse], error) {
	msg := req.Msg
	filter := issue.ListFilter{
		WorkspaceID: mustUUID(msg.WorkspaceId),
		PerPage:     50,
	}
	if msg.ProjectId != nil {
		id := mustUUID(*msg.ProjectId)
		filter.ProjectID = &id
	}
	if msg.PageSize != nil && *msg.PageSize > 0 {
		filter.PerPage = int(*msg.PageSize)
	}
	issues, total, err := h.issueSvc.Search(ctx, mustUUID(msg.WorkspaceId), msg.Query, filter)
	if err != nil {
		return nil, domainErr(err)
	}
	items := make([]*v1.Issue, len(issues))
	for i, iss := range issues {
		items[i] = protoIssue(iss)
	}
	return connect.NewResponse(&v1.SearchIssuesResponse{
		Issues: items,
		Total:  int32(total),
	}), nil
}

// ensure issuesvc import is used (service.IssueService satisfies issue.Service)
var _ issue.Service = (*issuesvc.IssueService)(nil)
