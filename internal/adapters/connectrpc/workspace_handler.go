package connectrpc

import (
	"context"

	"connectrpc.com/connect"
	v1 "github.com/agoodkind/tack/gen/tack/v1"
	"github.com/agoodkind/tack/gen/tack/v1/tackv1connect"
	"github.com/agoodkind/tack/internal/auth"
	"github.com/agoodkind/tack/internal/domain/workspace"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"
)

type WorkspaceHandler struct {
	workspaces workspace.Repository
}

var _ tackv1connect.WorkspaceServiceHandler = (*WorkspaceHandler)(nil)

func NewWorkspaceHandler(workspaces workspace.Repository) *WorkspaceHandler {
	return &WorkspaceHandler{workspaces: workspaces}
}

func (h *WorkspaceHandler) CreateWorkspace(ctx context.Context, req *connect.Request[v1.CreateWorkspaceRequest]) (*connect.Response[v1.Workspace], error) {
	userID := auth.MustUserID(ctx)
	msg := req.Msg
	w := &workspace.Workspace{
		ID:      uuid.New(),
		OrgID:   mustUUID(msg.OrgId),
		Name:    msg.Name,
		Slug:    msg.Slug,
	}
	_ = userID
	created, err := h.workspaces.Create(ctx, w)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoWorkspace(created)), nil
}

func (h *WorkspaceHandler) GetWorkspace(ctx context.Context, req *connect.Request[v1.GetWorkspaceRequest]) (*connect.Response[v1.Workspace], error) {
	w, err := h.workspaces.GetByID(ctx, mustUUID(req.Msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoWorkspace(w)), nil
}

func (h *WorkspaceHandler) ListWorkspaces(ctx context.Context, req *connect.Request[v1.ListWorkspacesRequest]) (*connect.Response[v1.ListWorkspacesResponse], error) {
	userID := auth.MustUserID(ctx)
	ws, err := h.workspaces.ListForUser(ctx, userID)
	if err != nil {
		return nil, domainErr(err)
	}
	items := make([]*v1.Workspace, len(ws))
	for i, w := range ws {
		items[i] = protoWorkspace(w)
	}
	return connect.NewResponse(&v1.ListWorkspacesResponse{Workspaces: items}), nil
}

func (h *WorkspaceHandler) UpdateWorkspace(_ context.Context, _ *connect.Request[v1.UpdateWorkspaceRequest]) (*connect.Response[v1.Workspace], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h *WorkspaceHandler) DeleteWorkspace(ctx context.Context, req *connect.Request[v1.DeleteWorkspaceRequest]) (*connect.Response[emptypb.Empty], error) {
	_ = req
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}
