package connectrpc

import (
	"context"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/agoodkind/tack/gen/tack/v1"
	"github.com/agoodkind/tack/gen/tack/v1/tackv1connect"
	"github.com/agoodkind/tack/internal/auth"
	"github.com/agoodkind/tack/internal/domain/cycle"
	"github.com/agoodkind/tack/internal/domain/node"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"
)

type CycleHandler struct {
	cycles      cycle.Repository
	containment node.ContainmentRepository
}

var _ tackv1connect.CycleServiceHandler = (*CycleHandler)(nil)

func NewCycleHandler(cycles cycle.Repository, containment node.ContainmentRepository) *CycleHandler {
	return &CycleHandler{cycles: cycles, containment: containment}
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
	c, err := h.cycles.GetByID(ctx, mustUUID(req.Msg.ProjectId), mustUUID(req.Msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoCycle(c)), nil
}

func (h *CycleHandler) ListCycles(ctx context.Context, req *connect.Request[v1.ListCyclesRequest]) (*connect.Response[v1.ListCyclesResponse], error) {
	cycles, err := h.cycles.List(ctx, mustUUID(req.Msg.ProjectId))
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
	c, err := h.cycles.GetByID(ctx, mustUUID(msg.ProjectId), mustUUID(msg.Id))
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
	if err := h.cycles.Delete(ctx, mustUUID(req.Msg.Id)); err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *CycleHandler) AddIssuesToCycle(ctx context.Context, req *connect.Request[v1.AddIssuesToCycleRequest]) (*connect.Response[emptypb.Empty], error) {
	userID := auth.MustUserID(ctx)
	msg := req.Msg
	orgID := mustUUID(msg.WorkspaceId) // org scoping — workspace ID used as org scope key
	cycleID := mustUUID(msg.CycleId)
	for _, issueIDStr := range msg.IssueIds {
		if err := h.containment.AddIssueToCycle(ctx, orgID, cycleID, mustUUID(issueIDStr), userID); err != nil {
			return nil, domainErr(err)
		}
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *CycleHandler) RemoveIssuesFromCycle(ctx context.Context, req *connect.Request[v1.RemoveIssuesFromCycleRequest]) (*connect.Response[emptypb.Empty], error) {
	msg := req.Msg
	orgID := mustUUID(msg.WorkspaceId)
	cycleID := mustUUID(msg.CycleId)
	for _, issueIDStr := range msg.IssueIds {
		if err := h.containment.RemoveIssueFromCycle(ctx, orgID, cycleID, mustUUID(issueIDStr)); err != nil {
			return nil, domainErr(err)
		}
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}
