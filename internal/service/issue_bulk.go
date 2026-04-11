package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/issue"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
)

func (s *IssueService) Move(ctx context.Context, workspaceID, projectID, issueID, targetProjectID uuid.UUID, actorID uuid.UUID) (*issue.Issue, error) {
	resolve, err := s.reader.Resolve(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	orgID := resolve.OrgID

	view, err := s.reader.Get(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("entity get: %w", err)
	}
	if view == nil {
		return nil, domain.ErrNotFound
	}

	i := issueFromNodeListView(view)
	i.ProjectID = targetProjectID
	i.StateID = nil
	i.UpdatedBy = &actorID
	i.UpdatedAt = time.Now().UTC()

	seqID, err := s.entities.AllocateSequenceID(ctx, orgID, targetProjectID, node.NodeTypeIssue)
	if err != nil {
		return nil, fmt.Errorf("allocate sequence: %w", err)
	}
	i.SequenceID = int(seqID)

	nv, props := nodeValueFromIssue(i, orgID)
	newView := buildIssueView(i, orgID)
	if err := s.entities.Set(ctx, nv, props, newView); err != nil {
		return nil, fmt.Errorf("entity set: %w", err)
	}

	_ = s.activity.Append(ctx, orgID, workspaceID, &node.ActivityEvent{
		EventID:   uuid.New(),
		NodeID:    issueID,
		Actor:     actorID,
		Verb:      "moved",
		Detail:    map[string]any{"target_project_id": targetProjectID},
		CreatedAt: i.UpdatedAt,
	})

	_ = s.searcher.Index(ctx, "nodes", i.ID.String(), nodeDocFromView(newView))

	telemetry.L(ctx).Info("issue.moved",
		slog.String("issue_id", issueID.String()),
		slog.String("from_project_id", projectID.String()),
		slog.String("to_project_id", targetProjectID.String()),
		slog.Int("new_sequence_id", int(seqID)),
	)
	return i, nil
}

func (s *IssueService) BulkUpdate(ctx context.Context, workspaceID uuid.UUID, patch issue.BulkUpdatePatch) (int, error) {
	resolve, err := s.reader.Resolve(ctx, workspaceID)
	if err != nil {
		return 0, fmt.Errorf("resolve workspace: %w", err)
	}
	orgID := resolve.OrgID

	var updated int
	for _, id := range patch.IssueIDs {
		view, getErr := s.reader.Get(ctx, id)
		if getErr != nil || view == nil {
			continue
		}
		if view.WorkspaceID != workspaceID || view.NodeType != node.NodeTypeIssue {
			continue
		}

		i := issueFromNodeListView(view)
		if patch.StateID != nil {
			i.StateID = patch.StateID
		}
		if patch.Priority != nil {
			i.Priority = *patch.Priority
		}
		if patch.SetEpicID {
			i.EpicID = patch.EpicID
		}
		if patch.AssigneeIDs != nil {
			i.AssigneeIDs = patch.AssigneeIDs
		}
		i.UpdatedBy = &patch.ActorID
		i.UpdatedAt = time.Now().UTC()

		newNV, newProps := nodeValueFromIssue(i, orgID)
		newView := buildIssueView(i, orgID)
		if setErr := s.entities.Set(ctx, newNV, newProps, newView); setErr != nil {
			continue
		}
		if patch.AssigneeIDs != nil {
			_ = s.assignments.SetAll(ctx, orgID, id, patch.AssigneeIDs, patch.ActorID)
		}
		_ = s.searcher.Index(ctx, "nodes", i.ID.String(), nodeDocFromView(newView))
		updated++
	}

	telemetry.L(ctx).Info("issue.bulk_updated",
		slog.Int("count", updated),
	)
	return updated, nil
}

func (s *IssueService) BulkDelete(ctx context.Context, workspaceID uuid.UUID, issueIDs []uuid.UUID) (int, error) {
	resolve, err := s.reader.Resolve(ctx, workspaceID)
	if err != nil {
		return 0, fmt.Errorf("resolve workspace: %w", err)
	}
	orgID := resolve.OrgID

	var deleted int
	for _, id := range issueIDs {
		nv, getErr := s.entities.Get(ctx, orgID, workspaceID, node.NodeTypeIssue, id)
		if getErr != nil || nv == nil {
			continue
		}
		if delErr := s.entities.Delete(ctx, nv); delErr != nil {
			continue
		}
		_ = s.nodeDeleter.DeleteNode(ctx, orgID, id)
		_ = s.searcher.Delete(ctx, "nodes", id.String())
		deleted++
	}

	telemetry.L(ctx).Info("issue.bulk_deleted",
		slog.Int("count", deleted),
	)
	return deleted, nil
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
