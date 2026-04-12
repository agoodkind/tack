package connectrpc

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"
	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/gen/tack/v1/tackv1connect"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"google.golang.org/protobuf/types/known/emptypb"
)

type ProjectHandler struct {
	reader     node.NodeReader
	entities   node.EntityRepository
	projectSvc *service.ProjectService
}

var _ tackv1connect.ProjectServiceHandler = (*ProjectHandler)(nil)

func NewProjectHandler(reader node.NodeReader, entities node.EntityRepository, svc *service.ProjectService) *ProjectHandler {
	return &ProjectHandler{reader: reader, entities: entities, projectSvc: svc}
}

func (h *ProjectHandler) CreateProject(ctx context.Context, req *connect.Request[v1.CreateProjectRequest]) (*connect.Response[v1.Project], error) {
	userID := auth.MustUserID(ctx)
	msg := req.Msg
	desc := ""
	if msg.Description != nil {
		desc = *msg.Description
	}
	view, err := h.projectSvc.Create(ctx, mustUUID(msg.WorkspaceId), msg.Name, msg.Identifier, desc, userID)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoProjectFromView(view)), nil
}

func (h *ProjectHandler) GetProject(ctx context.Context, req *connect.Request[v1.GetProjectRequest]) (*connect.Response[v1.Project], error) {
	view, err := h.reader.Get(ctx, mustUUID(req.Msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoProjectFromView(view)), nil
}

func (h *ProjectHandler) ListProjects(ctx context.Context, req *connect.Request[v1.ListProjectsRequest]) (*connect.Response[v1.ListProjectsResponse], error) {
	wsID := mustUUID(req.Msg.WorkspaceId)
	wsView, err := h.reader.Get(ctx, wsID)
	if err != nil {
		return nil, domainErr(err)
	}
	projects, err := h.reader.List(ctx, node.NodeListQuery{
		OrgID: wsView.OrgID, WorkspaceID: wsID, NodeType: node.NodeTypeProject,
	})
	if err != nil {
		return nil, domainErr(err)
	}
	items := make([]*v1.Project, len(projects))
	for i, p := range projects {
		items[i] = protoProjectFromView(p)
	}
	return connect.NewResponse(&v1.ListProjectsResponse{Projects: items}), nil
}

func (h *ProjectHandler) UpdateProject(ctx context.Context, req *connect.Request[v1.UpdateProjectRequest]) (*connect.Response[v1.Project], error) {
	msg := req.Msg
	projID := mustUUID(msg.Id)

	view, err := h.reader.Get(ctx, projID)
	if err != nil {
		return nil, domainErr(err)
	}

	if view.CustomProps == nil {
		view.CustomProps = make(map[string]json.RawMessage)
	}
	if msg.Name != nil {
		view.Name = *msg.Name
	}
	if msg.Description != nil {
		b, _ := json.Marshal(*msg.Description)
		view.CustomProps["description"] = b
	}
	if msg.DefaultStateId != nil {
		b, _ := json.Marshal(*msg.DefaultStateId)
		view.CustomProps["default_state_id"] = b
	}
	if msg.Identifier != nil {
		b, _ := json.Marshal(*msg.Identifier)
		view.CustomProps["identifier"] = b
	}

	nv := &node.NodeValue{
		ID: view.ID, OrgID: view.OrgID, WorkspaceID: view.WorkspaceID,
		ProjectID: view.ProjectID, NodeType: view.NodeType, Name: view.Name,
		CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt,
	}
	if err := h.entities.Set(ctx, nv, nil, view); err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoProjectFromView(view)), nil
}

func (h *ProjectHandler) DeleteProject(ctx context.Context, req *connect.Request[v1.DeleteProjectRequest]) (*connect.Response[emptypb.Empty], error) {
	view, err := h.reader.Get(ctx, mustUUID(req.Msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	nv := &node.NodeValue{
		ID: view.ID, OrgID: view.OrgID, WorkspaceID: view.WorkspaceID,
		ProjectID: view.ProjectID, NodeType: view.NodeType,
	}
	if err := h.entities.Delete(ctx, nv); err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}
