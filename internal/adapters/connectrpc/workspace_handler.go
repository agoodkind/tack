package connectrpc

import (
	"context"

	"connectrpc.com/connect"
	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/gen/tack/v1/tackv1connect"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"google.golang.org/protobuf/types/known/emptypb"
)

type WorkspaceHandler struct {
	workspaces *service.WorkspaceService
}

var _ tackv1connect.WorkspaceServiceHandler = (*WorkspaceHandler)(nil)

func NewWorkspaceHandler(workspaces *service.WorkspaceService) *WorkspaceHandler {
	return &WorkspaceHandler{workspaces: workspaces}
}

func (h *WorkspaceHandler) CreateWorkspace(ctx context.Context, req *connect.Request[v1.CreateWorkspaceRequest]) (*connect.Response[v1.Workspace], error) {
	_ = auth.MustUserID(ctx)
	msg := req.Msg
	created, err := h.workspaces.Create(ctx, mustUUID(msg.OrgId), msg.Name, msg.Slug)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoWorkspaceFromView(created)), nil
}

func (h *WorkspaceHandler) GetWorkspace(ctx context.Context, req *connect.Request[v1.GetWorkspaceRequest]) (*connect.Response[v1.Workspace], error) {
	w, err := h.workspaces.GetByID(ctx, mustUUID(req.Msg.Id))
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoWorkspaceFromView(w)), nil
}

func (h *WorkspaceHandler) ListWorkspaces(ctx context.Context, req *connect.Request[v1.ListWorkspacesRequest]) (*connect.Response[v1.ListWorkspacesResponse], error) {
	userID := auth.MustUserID(ctx)
	ws, err := h.workspaces.ListForUser(ctx, userID)
	if err != nil {
		return nil, domainErr(err)
	}
	items := make([]*v1.Workspace, len(ws))
	for i, w := range ws {
		items[i] = protoWorkspaceFromView(w)
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

func (h *WorkspaceHandler) DescribeWorkspace(ctx context.Context, req *connect.Request[v1.DescribeWorkspaceRequest]) (*connect.Response[v1.WorkspaceDescription], error) {
	ws, types, err := h.workspaces.Describe(ctx, req.Msg.Slug)
	if err != nil {
		return nil, domainErr(err)
	}
	ntDefs := make([]*v1.NodeTypeDefinition, len(types))
	for i, nt := range types {
		ntDefs[i] = protoNodeTypeDef(nt)
	}
	return connect.NewResponse(&v1.WorkspaceDescription{
		Workspace: protoWorkspaceFromView(ws),
		NodeTypes: ntDefs,
	}), nil
}

func protoNodeTypeDef(nt *node.NodeType) *v1.NodeTypeDefinition {
	ops := make([]string, len(nt.AllowedOps))
	for i, op := range nt.AllowedOps {
		ops[i] = string(op)
	}
	return &v1.NodeTypeDefinition{
		Id:         nt.ID.String(),
		OrgId:      nt.OrgID.String(),
		Name:       nt.Name,
		Slug:       nt.Slug,
		PluralSlug: nt.PluralSlug,
		IsBuiltin:  nt.IsBuiltin,
		TypeKey:    nt.TypeKey,
		Color:      nt.Color,
		Icon:       nt.Icon,
		AllowedOps: ops,
	}
}
