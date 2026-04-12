package connectrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/gen/tack/v1/tackv1connect"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"google.golang.org/protobuf/types/known/emptypb"
)

type LabelHandler struct {
	entities node.EntityRepository
	reader   node.NodeReader
}

var _ tackv1connect.LabelServiceHandler = (*LabelHandler)(nil)

func NewLabelHandler(entities node.EntityRepository, reader node.NodeReader) *LabelHandler {
	return &LabelHandler{entities: entities, reader: reader}
}

func (h *LabelHandler) CreateLabel(ctx context.Context, req *connect.Request[v1.CreateLabelRequest]) (*connect.Response[v1.Label], error) {
	msg := req.Msg
	wsID := mustUUID(msg.WorkspaceId)

	resolve, err := h.reader.Resolve(ctx, wsID)
	if err != nil {
		return nil, domainErr(err)
	}

	now := time.Now()
	id := uuid.New()
	orgID := resolve.OrgID
	var projID uuid.UUID
	if msg.ProjectId != nil {
		projID = mustUUID(*msg.ProjectId)
	}
	sortOrder := float64(65535)
	if msg.SortOrder != nil {
		sortOrder = float64(*msg.SortOrder)
	}

	colorBytes, _ := json.Marshal(msg.Color)
	sortBytes, _ := json.Marshal(sortOrder)

	nv := &node.NodeValue{
		ID: id, OrgID: orgID, WorkspaceID: wsID, ProjectID: projID,
		NodeType: node.NodeTypeLabel, Name: msg.Name, SortOrder: sortOrder,
		CreatedAt: now, UpdatedAt: now,
	}
	props := map[uuid.UUID]*node.PropertyValue{
		node.SystemPropID(wsID, "color"):      node.TextPropertyValue(msg.Color),
		node.SystemPropID(wsID, "sort_order"): node.TextPropertyValue(fmt.Sprintf("%v", sortOrder)),
	}
	view := &node.NodeListView{
		Version: node.ViewVersion1, ID: id, OrgID: orgID, WorkspaceID: wsID,
		ProjectID: projID, NodeType: node.NodeTypeLabel, Name: msg.Name,
		SortOrder: sortOrder, CreatedAt: now, UpdatedAt: now,
		CustomProps: map[string]json.RawMessage{
			"color": colorBytes, "sort_order": sortBytes,
		},
	}

	if _, err := h.entities.CreateAtomic(ctx, orgID, projID, nv, props, view, nil, nil, uuid.Nil); err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoLabelFromView(view)), nil
}

func (h *LabelHandler) GetLabel(ctx context.Context, req *connect.Request[v1.GetLabelRequest]) (*connect.Response[v1.Label], error) {
	view, err := h.reader.Get(ctx, mustUUID(req.Msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	if view == nil {
		return nil, domainErr(domain.ErrNotFound)
	}
	return connect.NewResponse(protoLabelFromView(view)), nil
}

func (h *LabelHandler) ListLabels(ctx context.Context, req *connect.Request[v1.ListLabelsRequest]) (*connect.Response[v1.ListLabelsResponse], error) {
	msg := req.Msg
	wsID := mustUUID(msg.WorkspaceId)
	resolve, err := h.reader.Resolve(ctx, wsID)
	if err != nil {
		return nil, domainErr(err)
	}
	q := node.NodeListQuery{
		OrgID: resolve.OrgID, WorkspaceID: wsID,
		NodeType: node.NodeTypeLabel,
	}
	if msg.ProjectId != nil {
		pid := mustUUID(*msg.ProjectId)
		q.ByProject = &pid
	}
	views, err := h.reader.List(ctx, q)
	if err != nil {
		return nil, domainErr(err)
	}
	items := make([]*v1.Label, len(views))
	for i, v := range views {
		items[i] = protoLabelFromView(v)
	}
	return connect.NewResponse(&v1.ListLabelsResponse{Labels: items}), nil
}

func (h *LabelHandler) UpdateLabel(ctx context.Context, req *connect.Request[v1.UpdateLabelRequest]) (*connect.Response[v1.Label], error) {
	msg := req.Msg
	id := mustUUID(msg.Id)

	view, err := h.reader.Get(ctx, id)
	if err != nil {
		return nil, domainErr(err)
	}
	if view == nil {
		return nil, domainErr(domain.ErrNotFound)
	}
	resolve, err := h.reader.Resolve(ctx, id)
	if err != nil {
		return nil, domainErr(err)
	}

	color := ""
	if view.CustomProps != nil {
		if raw, ok := view.CustomProps["color"]; ok {
			var s string
			_ = json.Unmarshal(raw, &s)
			color = s
		}
	}

	if msg.Name != nil {
		view.Name = *msg.Name
	}
	if msg.Color != nil {
		color = *msg.Color
	}
	if msg.SortOrder != nil {
		view.SortOrder = float64(*msg.SortOrder)
	}
	now := time.Now()
	view.UpdatedAt = now

	wsID := resolve.WorkspaceID
	colorBytes, _ := json.Marshal(color)
	if view.CustomProps == nil {
		view.CustomProps = make(map[string]json.RawMessage)
	}
	view.CustomProps["color"] = colorBytes

	nv := &node.NodeValue{
		ID: id, OrgID: resolve.OrgID, WorkspaceID: wsID,
		ProjectID: resolve.ProjectID, NodeType: node.NodeTypeLabel,
		Name: view.Name, SortOrder: view.SortOrder,
		CreatedAt: view.CreatedAt, UpdatedAt: now,
	}
	props := map[uuid.UUID]*node.PropertyValue{
		node.SystemPropID(wsID, "color"):      node.TextPropertyValue(color),
		node.SystemPropID(wsID, "sort_order"): node.TextPropertyValue(fmt.Sprintf("%v", view.SortOrder)),
	}

	if err := h.entities.Set(ctx, nv, props, view); err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoLabelFromView(view)), nil
}

func (h *LabelHandler) DeleteLabel(ctx context.Context, req *connect.Request[v1.DeleteLabelRequest]) (*connect.Response[emptypb.Empty], error) {
	id := mustUUID(req.Msg.Id)
	resolve, err := h.reader.Resolve(ctx, id)
	if err != nil {
		return nil, domainErr(err)
	}
	nv := &node.NodeValue{
		ID: id, OrgID: resolve.OrgID, WorkspaceID: resolve.WorkspaceID,
		ProjectID: resolve.ProjectID, NodeType: node.NodeTypeLabel,
	}
	if err := h.entities.Delete(ctx, nv); err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}
