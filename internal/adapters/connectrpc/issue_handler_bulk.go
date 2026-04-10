// Issue handler bulk and move operations.
package connectrpc

import (
	"context"

	"connectrpc.com/connect"
	v1 "github.com/agoodkind/tack/gen/tack/v1"
	"github.com/agoodkind/tack/internal/auth"
	"github.com/agoodkind/tack/internal/domain/issue"
)

// MoveIssue moves a single issue to a different project, reallocating its sequence ID.
func (h *IssueHandler) MoveIssue(ctx context.Context, req *connect.Request[v1.MoveIssueRequest]) (*connect.Response[v1.Issue], error) {
	userID := auth.MustUserID(ctx)
	msg := req.Msg
	i, err := h.issueSvc.Move(ctx,
		mustUUID(msg.WorkspaceId), mustUUID(msg.ProjectId),
		mustUUID(msg.IssueId), mustUUID(msg.TargetProjectId),
		userID,
	)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(protoIssue(i)), nil
}

// BulkUpdateIssues applies the same field changes to multiple issues atomically.
func (h *IssueHandler) BulkUpdateIssues(ctx context.Context, req *connect.Request[v1.BulkUpdateIssuesRequest]) (*connect.Response[v1.BulkUpdateIssuesResponse], error) {
	userID := auth.MustUserID(ctx)
	msg := req.Msg
	patch := issue.BulkUpdatePatch{
		IssueIDs:  parseUUIDs(msg.IssueIds),
		ProjectID: mustUUID(msg.ProjectId),
		ActorID:   userID,
	}
	if msg.StateId != nil {
		id := mustUUID(*msg.StateId)
		patch.StateID = &id
	}
	if msg.Priority != nil {
		p := issue.Priority(*msg.Priority)
		patch.Priority = &p
	}
	if msg.EpicId != nil || msg.ClearEpic {
		patch.SetEpicID = true
		patch.EpicID = optUUID(msg.EpicId)
	}
	if len(msg.AssigneeIds) > 0 {
		ids := parseUUIDs(msg.AssigneeIds)
		patch.AssigneeIDs = ids
	}
	updated, err := h.issueSvc.BulkUpdate(ctx, mustUUID(msg.WorkspaceId), patch)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(&v1.BulkUpdateIssuesResponse{Updated: int32(updated)}), nil
}

// BulkDeleteIssues soft-deletes multiple issues in one operation.
func (h *IssueHandler) BulkDeleteIssues(ctx context.Context, req *connect.Request[v1.BulkDeleteIssuesRequest]) (*connect.Response[v1.BulkDeleteIssuesResponse], error) {
	msg := req.Msg
	deleted, err := h.issueSvc.BulkDelete(ctx, mustUUID(msg.WorkspaceId), parseUUIDs(msg.IssueIds))
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(&v1.BulkDeleteIssuesResponse{Deleted: int32(deleted)}), nil
}

// BulkMoveIssues moves multiple issues to a different project in one operation.
func (h *IssueHandler) BulkMoveIssues(ctx context.Context, req *connect.Request[v1.BulkMoveIssuesRequest]) (*connect.Response[v1.BulkMoveIssuesResponse], error) {
	userID := auth.MustUserID(ctx)
	msg := req.Msg
	moved, failed, err := h.issueSvc.BulkMove(ctx,
		mustUUID(msg.WorkspaceId), mustUUID(msg.ProjectId),
		parseUUIDs(msg.IssueIds), mustUUID(msg.TargetProjectId),
		userID,
	)
	if err != nil {
		return nil, domainErr(err)
	}
	return connect.NewResponse(&v1.BulkMoveIssuesResponse{Moved: int32(moved), Failed: int32(failed)}), nil
}
