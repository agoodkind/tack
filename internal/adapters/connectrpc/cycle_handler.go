package connectrpc

import (
	"context"
	"time"

	"connectrpc.com/connect"
	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/gen/tack/v1/tackv1connect"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/cycle"
	"goodkind.io/tack/internal/service"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"
)

type CycleHandler struct {
	cycles *service.CycleService
}

var _ tackv1connect.CycleServiceHandler = (*CycleHandler)(nil)

func NewCycleHandler(cycles *service.CycleService) *CycleHandler {
	return &CycleHandler{cycles: cycles}
}

func (h *CycleHandler) CreateCycle(ctx context.Context, req *connect.Request[v1.CreateCycleRequest]) (*connect.Response[v1.Cycle], error) {
	userID := auth.MustUserID(ctx)
	msg := req.Msg
	c := &cycle.Cycle{
		ID:          uuid.New(),
		WorkspaceID: mustUUID(msg.WorkspaceId),
		ProjectID:   mustUUID(msg.ProjectId),
		Name:        msg.Name,
		CreatedBy:   userID,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		StartDate:   optTS(msg.StartDate),
		EndDate:     optTS(msg.EndDate),
	}
	if msg.Description != nil {
		c.Description = *msg.Description
	}
	created, err := h.cycles.Create(ctx, c)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoCycle(created)), nil
}

func (h *CycleHandler) GetCycle(ctx context.Context, req *connect.Request[v1.GetCycleRequest]) (*connect.Response[v1.Cycle], error) {
	msg := req.Msg
	c, err := h.cycles.GetByIDWithWorkspace(ctx, mustUUID(msg.WorkspaceId), mustUUID(msg.ProjectId), mustUUID(msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoCycle(c)), nil
}

func (h *CycleHandler) ListCycles(ctx context.Context, req *connect.Request[v1.ListCyclesRequest]) (*connect.Response[v1.ListCyclesResponse], error) {
	msg := req.Msg
	cycles, err := h.cycles.ListWithWorkspace(ctx, mustUUID(msg.WorkspaceId), mustUUID(msg.ProjectId))
	if err != nil {
		return nil, domainErr(err)
	}
	items := make([]*v1.Cycle, len(cycles))
	for i, c := range cycles {
		items[i] = protoCycle(c)
	}
	return connect.NewResponse(&v1.ListCyclesResponse{Cycles: items}), nil
}

func (h *CycleHandler) UpdateCycle(ctx context.Context, req *connect.Request[v1.UpdateCycleRequest]) (*connect.Response[v1.Cycle], error) {
	msg := req.Msg
	c, err := h.cycles.GetByIDWithWorkspace(ctx, mustUUID(msg.WorkspaceId), mustUUID(msg.ProjectId), mustUUID(msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	if msg.Name != nil {
		c.Name = *msg.Name
	}
	if msg.Description != nil {
		c.Description = *msg.Description
	}
	if msg.StartDate != nil {
		c.StartDate = optTS(msg.StartDate)
	}
	if msg.EndDate != nil {
		c.EndDate = optTS(msg.EndDate)
	}
	c.UpdatedAt = time.Now()
	updated, err := h.cycles.Update(ctx, c)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoCycle(updated)), nil
}

func (h *CycleHandler) DeleteCycle(ctx context.Context, req *connect.Request[v1.DeleteCycleRequest]) (*connect.Response[emptypb.Empty], error) {
	msg := req.Msg
	if err := h.cycles.DeleteByWorkspace(ctx, mustUUID(msg.WorkspaceId), mustUUID(msg.ProjectId), mustUUID(msg.Id)); err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *CycleHandler) AddIssuesToCycle(ctx context.Context, req *connect.Request[v1.AddIssuesToCycleRequest]) (*connect.Response[emptypb.Empty], error) {
	userID := auth.MustUserID(ctx)
	msg := req.Msg
	cycleID := mustUUID(msg.CycleId)
	issueIDs := make([]uuid.UUID, 0, len(msg.IssueIds))
	for _, id := range msg.IssueIds {
		issueIDs = append(issueIDs, mustUUID(id))
	}
	if err := h.cycles.AddIssues(ctx, cycleID, issueIDs, userID); err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *CycleHandler) RemoveIssuesFromCycle(ctx context.Context, req *connect.Request[v1.RemoveIssuesFromCycleRequest]) (*connect.Response[emptypb.Empty], error) {
	msg := req.Msg
	cycleID := mustUUID(msg.CycleId)
	issueIDs := make([]uuid.UUID, 0, len(msg.IssueIds))
	for _, id := range msg.IssueIds {
		issueIDs = append(issueIDs, mustUUID(id))
	}
	if err := h.cycles.RemoveIssues(ctx, cycleID, issueIDs); err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}
