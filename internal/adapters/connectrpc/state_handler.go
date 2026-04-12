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
	"goodkind.io/tack/internal/domain/state"
	"google.golang.org/protobuf/types/known/emptypb"
)

type StateHandler struct {
	entities node.EntityRepository
	reader   node.NodeReader
}

var _ tackv1connect.StateServiceHandler = (*StateHandler)(nil)

func NewStateHandler(entities node.EntityRepository, reader node.NodeReader) *StateHandler {
	return &StateHandler{entities: entities, reader: reader}
}

func (h *StateHandler) CreateState(ctx context.Context, req *connect.Request[v1.CreateStateRequest]) (*connect.Response[v1.State], error) {
	msg := req.Msg
	projID := mustUUID(msg.ProjectId)

	resolve, err := h.reader.Resolve(ctx, projID)
	if err != nil {
		return nil, domainErr(err)
	}

	now := time.Now()
	id := uuid.New()
	groupName := domainStateGroup(msg.Group)
	sortOrder := float64(65535)
	if msg.SortOrder != nil {
		sortOrder = float64(*msg.SortOrder)
	}

	orgID := resolve.OrgID
	wsID := resolve.WorkspaceID

	groupBytes, _ := json.Marshal(string(groupName))
	colorBytes, _ := json.Marshal(msg.Color)
	sortBytes, _ := json.Marshal(sortOrder)

	nv := &node.NodeValue{
		ID: id, OrgID: orgID, WorkspaceID: wsID, ProjectID: projID,
		NodeType: node.NodeTypeState, Name: msg.Name, SortOrder: sortOrder,
		CreatedAt: now, UpdatedAt: now,
	}
	props := map[uuid.UUID]*node.PropertyValue{
		node.SystemPropID(wsID, "group_name"): node.TextPropertyValue(string(groupName)),
		node.SystemPropID(wsID, "color"):      node.TextPropertyValue(msg.Color),
		node.SystemPropID(wsID, "sort_order"): node.TextPropertyValue(fmt.Sprintf("%v", sortOrder)),
	}
	view := &node.NodeListView{
		Version: node.ViewVersion1, ID: id, OrgID: orgID, WorkspaceID: wsID,
		ProjectID: projID, NodeType: node.NodeTypeState, Name: msg.Name,
		SortOrder: sortOrder, CreatedAt: now, UpdatedAt: now,
		CustomProps: map[string]json.RawMessage{
			"group_name": groupBytes, "color": colorBytes, "sort_order": sortBytes,
		},
	}

	if _, err := h.entities.CreateAtomic(ctx, orgID, projID, nv, props, view, nil, nil, uuid.Nil); err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoStateFromView(view)), nil
}

func (h *StateHandler) GetState(ctx context.Context, req *connect.Request[v1.GetStateRequest]) (*connect.Response[v1.State], error) {
	view, err := h.reader.Get(ctx, mustUUID(req.Msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	if view == nil {
		return nil, domainErr(domain.ErrNotFound)
	}
	return connect.NewResponse(protoStateFromView(view)), nil
}

func (h *StateHandler) ListStates(ctx context.Context, req *connect.Request[v1.ListStatesRequest]) (*connect.Response[v1.ListStatesResponse], error) {
	projID := mustUUID(req.Msg.ProjectId)
	resolve, err := h.reader.Resolve(ctx, projID)
	if err != nil {
		return nil, domainErr(err)
	}
	views, err := h.reader.List(ctx, node.NodeListQuery{
		OrgID: resolve.OrgID, WorkspaceID: resolve.WorkspaceID,
		NodeType: node.NodeTypeState, ByProject: &projID,
	})
	if err != nil {
		return nil, domainErr(err)
	}
	items := make([]*v1.State, len(views))
	for i, v := range views {
		items[i] = protoStateFromView(v)
	}
	return connect.NewResponse(&v1.ListStatesResponse{States: items}), nil
}

func (h *StateHandler) UpdateState(ctx context.Context, req *connect.Request[v1.UpdateStateRequest]) (*connect.Response[v1.State], error) {
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

	// Read existing custom props
	groupName := state.GroupBacklog
	color := ""
	if view.CustomProps != nil {
		if raw, ok := view.CustomProps["group_name"]; ok {
			var s string
			_ = json.Unmarshal(raw, &s)
			groupName = state.GroupName(s)
		}
		if raw, ok := view.CustomProps["color"]; ok {
			var s string
			_ = json.Unmarshal(raw, &s)
			color = s
		}
	}

	if msg.Name != nil {
		view.Name = *msg.Name
	}
	if msg.Group != nil {
		groupName = domainStateGroup(*msg.Group)
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
	groupBytes, _ := json.Marshal(string(groupName))
	colorBytes, _ := json.Marshal(color)
	if view.CustomProps == nil {
		view.CustomProps = make(map[string]json.RawMessage)
	}
	view.CustomProps["group_name"] = groupBytes
	view.CustomProps["color"] = colorBytes

	nv := &node.NodeValue{
		ID: id, OrgID: resolve.OrgID, WorkspaceID: wsID,
		ProjectID: resolve.ProjectID, NodeType: node.NodeTypeState,
		Name: view.Name, SortOrder: view.SortOrder,
		CreatedAt: view.CreatedAt, UpdatedAt: now,
	}
	props := map[uuid.UUID]*node.PropertyValue{
		node.SystemPropID(wsID, "group_name"): node.TextPropertyValue(string(groupName)),
		node.SystemPropID(wsID, "color"):      node.TextPropertyValue(color),
		node.SystemPropID(wsID, "sort_order"): node.TextPropertyValue(fmt.Sprintf("%v", view.SortOrder)),
	}

	if err := h.entities.Set(ctx, nv, props, view); err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoStateFromView(view)), nil
}

func (h *StateHandler) DeleteState(ctx context.Context, req *connect.Request[v1.DeleteStateRequest]) (*connect.Response[emptypb.Empty], error) {
	id := mustUUID(req.Msg.Id)
	resolve, err := h.reader.Resolve(ctx, id)
	if err != nil {
		return nil, domainErr(err)
	}
	nv := &node.NodeValue{
		ID: id, OrgID: resolve.OrgID, WorkspaceID: resolve.WorkspaceID,
		ProjectID: resolve.ProjectID, NodeType: node.NodeTypeState,
	}
	if err := h.entities.Delete(ctx, nv); err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}
