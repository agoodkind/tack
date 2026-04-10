package connectrpc

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"
	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/gen/tack/v1/tackv1connect"
	"goodkind.io/tack/internal/domain/node"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ActivityHandler struct {
	activity node.ActivityRepository
}

var _ tackv1connect.ActivityServiceHandler = (*ActivityHandler)(nil)

func NewActivityHandler(activity node.ActivityRepository) *ActivityHandler {
	return &ActivityHandler{activity: activity}
}

func (h *ActivityHandler) ListActivity(ctx context.Context, req *connect.Request[v1.ListActivityRequest]) (*connect.Response[v1.ListActivityResponse], error) {
	msg := req.Msg
	workspaceID := mustUUID(msg.WorkspaceId)
	nodeID := mustUUID(msg.NodeId)
	// org scoping: use workspaceID as orgID (single-org deployment for now)
	events, err := h.activity.List(ctx, workspaceID, workspaceID, nodeID)
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
			OrgId:      workspaceID.String(),
			NodeId:     e.NodeID.String(),
			Verb:       e.Verb,
			DetailJson: detail,
			ActorId:    e.Actor.String(),
			CreatedAt:  timestamppb.New(e.CreatedAt),
		})
	}
	return connect.NewResponse(&v1.ListActivityResponse{Events: items}), nil
}
