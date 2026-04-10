package connectrpc

import (
	"context"
	"time"

	"connectrpc.com/connect"
	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/gen/tack/v1/tackv1connect"
	"goodkind.io/tack/internal/domain/label"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"
)

type LabelHandler struct {
	labels label.Repository
}

var _ tackv1connect.LabelServiceHandler = (*LabelHandler)(nil)

func NewLabelHandler(labels label.Repository) *LabelHandler {
	return &LabelHandler{labels: labels}
}

func (h *LabelHandler) CreateLabel(ctx context.Context, req *connect.Request[v1.CreateLabelRequest]) (*connect.Response[v1.Label], error) {
	msg := req.Msg
	l := &label.Label{
		ID:          uuid.New(),
		WorkspaceID: mustUUID(msg.WorkspaceId),
		ProjectID:   optUUID(msg.ProjectId),
		Name:        msg.Name,
		Color:       msg.Color,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if msg.SortOrder != nil {
		l.SortOrder = float64(*msg.SortOrder)
	}
	created, err := h.labels.Create(ctx, l)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoLabel(created)), nil
}

func (h *LabelHandler) GetLabel(ctx context.Context, req *connect.Request[v1.GetLabelRequest]) (*connect.Response[v1.Label], error) {
	l, err := h.labels.GetByID(ctx, mustUUID(req.Msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoLabel(l)), nil
}

func (h *LabelHandler) ListLabels(ctx context.Context, req *connect.Request[v1.ListLabelsRequest]) (*connect.Response[v1.ListLabelsResponse], error) {
	msg := req.Msg
	labels, err := h.labels.List(ctx, mustUUID(msg.WorkspaceId), optUUID(msg.ProjectId))
	if err != nil {
		return nil, domainErr(err)
	}
	items := make([]*v1.Label, len(labels))
	for i, l := range labels {
		items[i] = protoLabel(l)
	}
	return connect.NewResponse(&v1.ListLabelsResponse{Labels: items}), nil
}

func (h *LabelHandler) UpdateLabel(ctx context.Context, req *connect.Request[v1.UpdateLabelRequest]) (*connect.Response[v1.Label], error) {
	msg := req.Msg
	l, err := h.labels.GetByID(ctx, mustUUID(msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	if msg.Name != nil {
		l.Name = *msg.Name
	}
	if msg.Color != nil {
		l.Color = *msg.Color
	}
	if msg.SortOrder != nil {
		l.SortOrder = float64(*msg.SortOrder)
	}
	l.UpdatedAt = time.Now()
	updated, err := h.labels.Update(ctx, l)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoLabel(updated)), nil
}

func (h *LabelHandler) DeleteLabel(ctx context.Context, req *connect.Request[v1.DeleteLabelRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := h.labels.Delete(ctx, mustUUID(req.Msg.Id)); err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}
