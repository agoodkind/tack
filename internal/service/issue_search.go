package service

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/domain/issue"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
)

// issueIndexDoc is the subset of issue fields indexed in Meilisearch.
// The id field must match domain.Base.ID so Meilisearch can use it as the primary key.
type issueIndexDoc struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Priority    string `json:"priority"`
}

// issueDoc builds an issueIndexDoc from a domain issue for indexing.
func issueDoc(i *issue.Issue) issueIndexDoc {
	return issueIndexDoc{
		ID:          i.ID.String(),
		WorkspaceID: i.WorkspaceID.String(),
		ProjectID:   i.ProjectID.String(),
		Name:        i.Name,
		Description: i.Description,
		Priority:    string(i.Priority),
	}
}

// Search queries Meilisearch first, then hydrates from SQL.
// Falls back to SQL ILIKE when Meilisearch is unavailable (Noop returns nil).
func (s *IssueService) Search(ctx context.Context, workspaceID uuid.UUID, q string, filter issue.ListFilter) ([]*issue.Issue, int, error) {
	ids, err := s.searcher.Search(ctx, "issues", q, map[string]string{
		"workspace_id": workspaceID.String(),
	})
	if err != nil {
		telemetry.L(ctx).Error("search.meilisearch_failed",
			slog.String("err", err.Error()),
			slog.String("workspace_id", workspaceID.String()),
		)
		// Fall back to SQL on error.
		return s.issues.Search(ctx, workspaceID, q, filter)
	}
	if ids == nil {
		// Noop searcher: fall back to SQL ILIKE.
		return s.issues.Search(ctx, workspaceID, q, filter)
	}
	if len(ids) == 0 {
		return nil, 0, nil
	}
	issueIDs := make([]uuid.UUID, 0, len(ids))
	for _, rawID := range ids {
		parsed, parseErr := uuid.Parse(rawID)
		if parseErr == nil {
			issueIDs = append(issueIDs, parsed)
		}
	}
	if len(issueIDs) == 0 {
		return nil, 0, nil
	}
	issues, err := s.issues.GetByIDs(ctx, workspaceID, issueIDs)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch search results: %w", err)
	}
	return issues, len(issues), nil
}
