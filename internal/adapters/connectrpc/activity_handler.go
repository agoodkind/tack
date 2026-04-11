package connectrpc

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"
	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/gen/tack/v1/tackv1connect"
	"goodkind.io/tack/internal/service"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ActivityHandler struct {
	issueSvc *service.IssueService
}

var _ tackv1connect.ActivityServiceHandler = (*ActivityHandler)(nil)

func NewActivityHandler(issueSvc *service.IssueService) *ActivityHandler {
	return &ActivityHandler{issueSvc: issueSvc}
}

func (h *ActivityHandler) ListActivity(ctx context.Context, req *connect.Request[v1.ListActivityRequest]) (*connect.Response[v1.ListActivityResponse], error) {
	msg := req.Msg
	nodeID := mustUUID(msg.NodeId)
	events, err := h.issueSvc.ListActivity(ctx, nodeID)
	if err != nil {
		return nil, domainErr(err)
	}
	items := make([]*v1.ActivityEvent, 0, len(events))
	for _, e := range events {
		detail := "{}"
		if len(e.Detail) > 0 {
			if b, err := json.Marshal(e.Detail); err == nil {
				detail = string(b)
			}
		}
		items = append(items, &v1.ActivityEvent{
			Id:         e.EventID.String(),
			OrgId:      mustUUID(msg.WorkspaceId).String(),
			NodeId:     e.NodeID.String(),
			Verb:       e.Verb,
			DetailJson: detail,
			ActorId:    e.Actor.String(),
			CreatedAt:  timestamppb.New(e.CreatedAt),
		})
	}
	return connect.NewResponse(&v1.ListActivityResponse{Events: items}), nil
}
