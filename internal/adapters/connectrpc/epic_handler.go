package connectrpc

import (
	"context"

	"connectrpc.com/connect"
	v1 "github.com/agoodkind/tack/gen/tack/v1"
	"github.com/agoodkind/tack/gen/tack/v1/tackv1connect"
	"github.com/agoodkind/tack/internal/auth"
	"github.com/agoodkind/tack/internal/domain/epic"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"
)

type EpicHandler struct {
	epics epic.Repository
}

var _ tackv1connect.EpicServiceHandler = (*EpicHandler)(nil)

func NewEpicHandler(epics epic.Repository) *EpicHandler {
	return &EpicHandler{epics: epics}
}

func (h *EpicHandler) CreateEpic(ctx context.Context, req *connect.Request[v1.CreateEpicRequest]) (*connect.Response[v1.Epic], error) {
	userID := auth.MustUserID(ctx)
	msg := req.Msg
	e := &epic.Epic{
		WorkspaceID: mustUUID(msg.WorkspaceId),
		ProjectID:   mustUUID(msg.ProjectId),
		Name:        msg.Name,
	}
	e.ID = uuid.New()
	e.CreatedBy = userID
	if msg.Description != nil {
		e.Description = *msg.Description
	}
	if msg.Priority != nil {
		e.Priority = domainPriority(*msg.Priority)
	}
	if msg.StateId != nil {
		id := mustUUID(*msg.StateId)
		e.StateID = &id
	}
	if msg.ParentId != nil {
		id := mustUUID(*msg.ParentId)
		e.ParentID = &id
	}
	created, err := h.epics.Create(ctx, e)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoEpic(created)), nil
}

func (h *EpicHandler) GetEpic(ctx context.Context, req *connect.Request[v1.GetEpicRequest]) (*connect.Response[v1.Epic], error) {
	e, err := h.epics.GetByID(ctx, mustUUID(req.Msg.WorkspaceId), mustUUID(req.Msg.ProjectId), mustUUID(req.Msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoEpic(e)), nil
}

func (h *EpicHandler) ListEpics(ctx context.Context, req *connect.Request[v1.ListEpicsRequest]) (*connect.Response[v1.ListEpicsResponse], error) {
	epics, _, err := h.epics.List(ctx, mustUUID(req.Msg.WorkspaceId), mustUUID(req.Msg.ProjectId))
	if err != nil {
		return nil, domainErr(err)
	}
	items := make([]*v1.Epic, len(epics))
	for i, e := range epics {
		items[i] = protoEpic(e)
	}
	return connect.NewResponse(&v1.ListEpicsResponse{Epics: items}), nil
}

func (h *EpicHandler) UpdateEpic(ctx context.Context, req *connect.Request[v1.UpdateEpicRequest]) (*connect.Response[v1.Epic], error) {
	msg := req.Msg
	e, err := h.epics.GetByID(ctx, mustUUID(msg.WorkspaceId), mustUUID(msg.ProjectId), mustUUID(msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	if msg.Name != nil {
		e.Name = *msg.Name
	}
	if msg.Description != nil {
		e.Description = *msg.Description
	}
	if msg.Priority != nil {
		e.Priority = domainPriority(*msg.Priority)
	}
	if msg.StateId != nil {
		id := mustUUID(*msg.StateId)
		e.StateID = &id
	}
	updated, err := h.epics.Update(ctx, e)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoEpic(updated)), nil
}

func (h *EpicHandler) DeleteEpic(ctx context.Context, req *connect.Request[v1.DeleteEpicRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := h.epics.Delete(ctx, mustUUID(req.Msg.Id)); err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}
