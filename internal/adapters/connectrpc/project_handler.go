package connectrpc

import (
	"context"

	"connectrpc.com/connect"
	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/gen/tack/v1/tackv1connect"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/project"
	"goodkind.io/tack/internal/service"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"
)

type ProjectHandler struct {
	projects   project.Repository
	projectSvc *service.ProjectService
}

var _ tackv1connect.ProjectServiceHandler = (*ProjectHandler)(nil)

func NewProjectHandler(projects project.Repository, svc *service.ProjectService) *ProjectHandler {
	return &ProjectHandler{projects: projects, projectSvc: svc}
}

func (h *ProjectHandler) CreateProject(ctx context.Context, req *connect.Request[v1.CreateProjectRequest]) (*connect.Response[v1.Project], error) {
	userID := auth.MustUserID(ctx)
	msg := req.Msg
	p := &project.Project{
		ID:          uuid.New(),
		WorkspaceID: mustUUID(msg.WorkspaceId),
		Name:        msg.Name,
		Identifier:  msg.Identifier,
		CreatedBy:   userID,
	}
	if msg.Description != nil {
		p.Description = *msg.Description
	}
	created, err := h.projectSvc.Create(ctx, p)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoProject(created)), nil
}

func (h *ProjectHandler) GetProject(ctx context.Context, req *connect.Request[v1.GetProjectRequest]) (*connect.Response[v1.Project], error) {
	p, err := h.projects.GetByID(ctx, mustUUID(req.Msg.WorkspaceId), mustUUID(req.Msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoProject(p)), nil
}

func (h *ProjectHandler) ListProjects(ctx context.Context, req *connect.Request[v1.ListProjectsRequest]) (*connect.Response[v1.ListProjectsResponse], error) {
	projects, err := h.projects.List(ctx, mustUUID(req.Msg.WorkspaceId))
	if err != nil {
		return nil, domainErr(err)
	}
	items := make([]*v1.Project, len(projects))
	for i, p := range projects {
		items[i] = protoProject(p)
	}
	return connect.NewResponse(&v1.ListProjectsResponse{Projects: items}), nil
}

func (h *ProjectHandler) UpdateProject(ctx context.Context, req *connect.Request[v1.UpdateProjectRequest]) (*connect.Response[v1.Project], error) {
	msg := req.Msg
	p, err := h.projects.GetByID(ctx, mustUUID(msg.WorkspaceId), mustUUID(msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	if msg.Name != nil {
		p.Name = *msg.Name
	}
	if msg.Description != nil {
		p.Description = *msg.Description
	}
	if msg.DefaultStateId != nil {
		id := mustUUID(*msg.DefaultStateId)
		p.DefaultStateID = &id
	}
	if msg.Identifier != nil {
		p.Identifier = *msg.Identifier
	}
	updated, err := h.projects.Update(ctx, p)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoProject(updated)), nil
}

func (h *ProjectHandler) DeleteProject(ctx context.Context, req *connect.Request[v1.DeleteProjectRequest]) (*connect.Response[emptypb.Empty], error) {
	err := h.projects.Delete(ctx, mustUUID(req.Msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}
