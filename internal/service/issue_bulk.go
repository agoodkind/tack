package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/domain/issue"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
)

func (s *IssueService) Move(ctx context.Context, workspaceID, projectID, issueID, targetProjectID uuid.UUID, actorID uuid.UUID) (*issue.Issue, error) {
	existing, err := s.issues.GetByID(ctx, workspaceID, projectID, issueID)
	if err != nil {
		return nil, err
	}
	seq, err := s.projects.AllocateSequenceID(ctx, targetProjectID, "issue")
	if err != nil {
		return nil, fmt.Errorf("allocate sequence: %w", err)
	}
	moved, err := s.issues.Move(ctx, existing.ID, targetProjectID, seq, actorID)
	if err != nil {
		return nil, err
	}

	ws, err := s.workspaces.GetByID(ctx, workspaceID)
	if err != nil {
		return moved, nil
	}
	_ = s.activity.Append(ctx, ws.OrgID, workspaceID, &node.ActivityEvent{
		EventID:   uuid.New(),
		NodeID:    existing.NodeID,
		Actor:     actorID,
		Verb:      "moved",
		Detail:    map[string]any{"target_project_id": targetProjectID},
		CreatedAt: time.Now().UTC(),
	})

	_ = s.searcher.Index(ctx, "issues", moved.ID.String(), issueDoc(moved))

	telemetry.L(ctx).Info("issue.moved",
		slog.String("issue_id", issueID.String()),
		slog.String("from_project_id", projectID.String()),
		slog.String("to_project_id", targetProjectID.String()),
		slog.Int("new_sequence_id", seq),
	)
	return moved, nil
}

func (s *IssueService) BulkUpdate(ctx context.Context, workspaceID uuid.UUID, patch issue.BulkUpdatePatch) (int, error) {
	updated, err := s.issues.BulkUpdate(ctx, patch)
	if err != nil {
		return 0, err
	}
	// Update FDB assignees per issue if a replacement set was provided.
	if patch.AssigneeIDs != nil {
		ws, wsErr := s.workspaces.GetByID(ctx, workspaceID)
		if wsErr == nil {
			for _, id := range patch.IssueIDs {
				i, getErr := s.issues.GetByID(ctx, workspaceID, patch.ProjectID, id)
				if getErr != nil {
					continue
				}
				_ = s.assignments.SetAll(ctx, ws.OrgID, i.NodeID, patch.AssigneeIDs, patch.ActorID)
			}
		}
	}
	telemetry.L(ctx).Info("issue.bulk_updated",
		slog.Int("count", updated),
	)
	return updated, nil
}

func (s *IssueService) BulkDelete(ctx context.Context, workspaceID uuid.UUID, issueIDs []uuid.UUID) (int, error) {
	nodeIDs, err := s.issues.BulkDelete(ctx, issueIDs)
	if err != nil {
		return 0, err
	}
	ws, wsErr := s.workspaces.GetByID(ctx, workspaceID)
	if wsErr == nil {
		for _, nodeID := range nodeIDs {
			_ = s.nodeDeleter.DeleteNode(ctx, ws.OrgID, nodeID)
		}
	}
	for _, id := range issueIDs {
		_ = s.searcher.Delete(ctx, "issues", id.String())
	}
	telemetry.L(ctx).Info("issue.bulk_deleted",
		slog.Int("count", len(nodeIDs)),
	)
	return len(nodeIDs), nil
}

func (s *IssueService) BulkMove(ctx context.Context, workspaceID, projectID uuid.UUID, issueIDs []uuid.UUID, targetProjectID uuid.UUID, actorID uuid.UUID) (int, int, error) {
	var moved, failed int
	for _, id := range issueIDs {
		if _, err := s.Move(ctx, workspaceID, projectID, id, targetProjectID, actorID); err != nil {
			failed++
		} else {
			moved++
		}
	}
	telemetry.L(ctx).Info("issue.bulk_moved",
		slog.Int("moved", moved),
		slog.Int("failed", failed),
	)
	return moved, failed, nil
}
