package connectrpc

import (
	"context"
	"time"

	"connectrpc.com/connect"
	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/gen/tack/v1/tackv1connect"
	"goodkind.io/tack/internal/domain/state"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"
)

type StateHandler struct {
	states state.Repository
}

var _ tackv1connect.StateServiceHandler = (*StateHandler)(nil)

func NewStateHandler(states state.Repository) *StateHandler {
	return &StateHandler{states: states}
}

func (h *StateHandler) CreateState(ctx context.Context, req *connect.Request[v1.CreateStateRequest]) (*connect.Response[v1.State], error) {
	msg := req.Msg
	s := &state.State{
		ID:        uuid.New(),
		ProjectID: mustUUID(msg.ProjectId),
		Name:      msg.Name,
		GroupName: domainStateGroup(msg.Group),
		Color:     msg.Color,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if msg.SortOrder != nil {
		s.SortOrder = float64(*msg.SortOrder)
	}
	created, err := h.states.Create(ctx, s)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoState(created)), nil
}

func (h *StateHandler) GetState(ctx context.Context, req *connect.Request[v1.GetStateRequest]) (*connect.Response[v1.State], error) {
	s, err := h.states.GetByID(ctx, mustUUID(req.Msg.ProjectId), mustUUID(req.Msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoState(s)), nil
}

func (h *StateHandler) ListStates(ctx context.Context, req *connect.Request[v1.ListStatesRequest]) (*connect.Response[v1.ListStatesResponse], error) {
	states, err := h.states.List(ctx, mustUUID(req.Msg.ProjectId))
	if err != nil {
		return nil, domainErr(err)
	}
	items := make([]*v1.State, len(states))
	for i, s := range states {
		items[i] = protoState(s)
	}
	return connect.NewResponse(&v1.ListStatesResponse{States: items}), nil
}

func (h *StateHandler) UpdateState(ctx context.Context, req *connect.Request[v1.UpdateStateRequest]) (*connect.Response[v1.State], error) {
	msg := req.Msg
	s, err := h.states.GetByID(ctx, mustUUID(msg.ProjectId), mustUUID(msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	if msg.Name != nil {
		s.Name = *msg.Name
	}
	if msg.Group != nil {
		s.GroupName = domainStateGroup(*msg.Group)
	}
	if msg.Color != nil {
		s.Color = *msg.Color
	}
	if msg.SortOrder != nil {
		s.SortOrder = float64(*msg.SortOrder)
	}
	s.UpdatedAt = time.Now()
	updated, err := h.states.Update(ctx, s)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoState(updated)), nil
}

func (h *StateHandler) DeleteState(ctx context.Context, req *connect.Request[v1.DeleteStateRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := h.states.Delete(ctx, mustUUID(req.Msg.Id)); err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}
