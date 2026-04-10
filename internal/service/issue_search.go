package service

import (
	"context"
	"fmt"
	"log/slog"

	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain/issue"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
)

// issueDoc builds a NodeDoc from a domain issue for indexing.
func issueDoc(i *issue.Issue) domainsearch.NodeDoc {
	return domainsearch.NodeDoc{
		ID:          i.ID.String(),
		NodeID:      i.NodeID.String(),
		WorkspaceID: i.WorkspaceID.String(),
		ProjectID:   i.ProjectID.String(),
		EntityType:  "issue",
		Name:        i.Name,
		Description: i.Description,
	}
}

// Search queries Meilisearch first, then hydrates from SQL.
// Falls back to SQL ILIKE when Meilisearch is unavailable (Noop returns nil).
func (s *IssueService) Search(ctx context.Context, workspaceID uuid.UUID, q string, filter issue.ListFilter) ([]*issue.Issue, int, error) {
	docs, err := s.searcher.Search(ctx, "nodes", q, map[string]string{
		"workspace_id": workspaceID.String(),
		"entity_type":  "issue",
	})
	if err != nil {
		telemetry.L(ctx).Error("search.meilisearch_failed",
			slog.String("err", err.Error()),
			slog.String("workspace_id", workspaceID.String()),
		)
		// Fall back to SQL on error.
		return s.issues.Search(ctx, workspaceID, q, filter)
	}
	if docs == nil {
		// Noop searcher: fall back to SQL ILIKE.
		return s.issues.Search(ctx, workspaceID, q, filter)
	}
	if len(docs) == 0 {
		return nil, 0, nil
	}
	issueIDs := make([]uuid.UUID, 0, len(docs))
	for _, doc := range docs {
		parsed, parseErr := uuid.Parse(doc.ID)
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
