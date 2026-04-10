package connectrpc

import (
	"context"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/agoodkind/tack/gen/tack/v1"
	"github.com/agoodkind/tack/gen/tack/v1/tackv1connect"
	"github.com/agoodkind/tack/internal/auth"
	"github.com/agoodkind/tack/internal/domain/module"
	"github.com/agoodkind/tack/internal/domain/node"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"
)

type ModuleHandler struct {
	modules     module.Repository
	containment node.ContainmentRepository
}

var _ tackv1connect.ModuleServiceHandler = (*ModuleHandler)(nil)

func NewModuleHandler(modules module.Repository, containment node.ContainmentRepository) *ModuleHandler {
	return &ModuleHandler{modules: modules, containment: containment}
}

func (h *ModuleHandler) CreateModule(ctx context.Context, req *connect.Request[v1.CreateModuleRequest]) (*connect.Response[v1.Module], error) {
	userID := auth.MustUserID(ctx)
	msg := req.Msg
	m := &module.Module{
		ID:          uuid.New(),
		WorkspaceID: mustUUID(msg.WorkspaceId),
		ProjectID:   mustUUID(msg.ProjectId),
		Name:        msg.Name,
		CreatedBy:   userID,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		StartDate:   optTS(msg.StartDate),
		TargetDate:  optTS(msg.TargetDate),
	}
	if msg.Description != nil {
		m.Description = *msg.Description
	}
	if msg.Status != nil {
		m.Status = *msg.Status
	}
	created, err := h.modules.Create(ctx, m)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoModule(created)), nil
}

func (h *ModuleHandler) GetModule(ctx context.Context, req *connect.Request[v1.GetModuleRequest]) (*connect.Response[v1.Module], error) {
	m, err := h.modules.GetByID(ctx, mustUUID(req.Msg.ProjectId), mustUUID(req.Msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoModule(m)), nil
}

func (h *ModuleHandler) ListModules(ctx context.Context, req *connect.Request[v1.ListModulesRequest]) (*connect.Response[v1.ListModulesResponse], error) {
	modules, err := h.modules.List(ctx, mustUUID(req.Msg.ProjectId))
	if err != nil {
		return nil, domainErr(err)
	}
	items := make([]*v1.Module, len(modules))
	for i, m := range modules {
		items[i] = protoModule(m)
	}
	return connect.NewResponse(&v1.ListModulesResponse{Modules: items}), nil
}

func (h *ModuleHandler) UpdateModule(ctx context.Context, req *connect.Request[v1.UpdateModuleRequest]) (*connect.Response[v1.Module], error) {
	msg := req.Msg
	m, err := h.modules.GetByID(ctx, mustUUID(msg.ProjectId), mustUUID(msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	if msg.Name != nil {
		m.Name = *msg.Name
	}
	if msg.Description != nil {
		m.Description = *msg.Description
	}
	if msg.Status != nil {
		m.Status = *msg.Status
	}
	if msg.StartDate != nil {
		m.StartDate = optTS(msg.StartDate)
	}
	if msg.TargetDate != nil {
		m.TargetDate = optTS(msg.TargetDate)
	}
	m.UpdatedAt = time.Now()
	updated, err := h.modules.Update(ctx, m)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoModule(updated)), nil
}

func (h *ModuleHandler) DeleteModule(ctx context.Context, req *connect.Request[v1.DeleteModuleRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := h.modules.Delete(ctx, mustUUID(req.Msg.Id)); err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *ModuleHandler) AddIssuesToModule(ctx context.Context, req *connect.Request[v1.AddIssuesToModuleRequest]) (*connect.Response[emptypb.Empty], error) {
	userID := auth.MustUserID(ctx)
	msg := req.Msg
	orgID := mustUUID(msg.WorkspaceId)
	moduleID := mustUUID(msg.ModuleId)
	for _, issueIDStr := range msg.IssueIds {
		if err := h.containment.AddIssueToModule(ctx, orgID, moduleID, mustUUID(issueIDStr), userID); err != nil {
			return nil, domainErr(err)
		}
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *ModuleHandler) RemoveIssuesFromModule(ctx context.Context, req *connect.Request[v1.RemoveIssuesFromModuleRequest]) (*connect.Response[emptypb.Empty], error) {
	msg := req.Msg
	orgID := mustUUID(msg.WorkspaceId)
	moduleID := mustUUID(msg.ModuleId)
	for _, issueIDStr := range msg.IssueIds {
		if err := h.containment.RemoveIssueFromModule(ctx, orgID, moduleID, mustUUID(issueIDStr)); err != nil {
			return nil, domainErr(err)
		}
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}
