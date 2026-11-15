package service

import (
	"context"
	"log/slog"

	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// replay rebuilds the page of the cursor at version from its committed
// result IDs. It rereads current summaries and access keys with the same
// summary reader that page reads use. It omits each committed node that is
// deleted, is currently forbidden, or no longer fits the response budget. It
// neither advances nor renews the session.
func (s *SearchQueryService) replay(
	ctx context.Context,
	session searchdomain.Session,
	version uint64,
	filter searchdomain.AccessFilter,
) (SearchPage, error) {
	commit, found, err := s.ports.Sessions.Replay(ctx, session.ID, version)
	if err != nil {
		return SearchPage{}, queryFailure(ctx, "read replay", session.ID, err)
	}

	if !found {
		return SearchPage{}, queryFailure(
			ctx,
			"read replay",
			session.ID,
			searchdomain.ErrSessionChanged,
		)
	}

	summaries, err := s.ports.Summaries.Summaries(ctx, commit.ResultIDs, summaryNameBytes)
	if err != nil {
		return SearchPage{}, queryFailure(ctx, "read replay summaries", session.ID, err)
	}

	results := make([]node.Summary, 0, len(summaries))
	used := responseReserveBytes

	for _, summary := range summaries {
		if !accepted(summary, filter, session.Query.NodeType) {
			continue
		}

		size := ResultBytes(summary.Summary)
		if used+size > s.settings.MaxResponseBytes {
			continue
		}

		results = append(results, summary.Summary)
		used += size
	}

	telemetry.L(ctx).InfoContext(
		ctx,
		"search.page.replayed",
		slog.String("session_id", session.ID.String()),
		slog.Uint64("version", version),
		slog.Int("results", len(results)),
		slog.Int("withheld", len(commit.ResultIDs)-len(results)),
	)

	return SearchPage{
		SessionID:   session.ID,
		NextVersion: version + 1,
		Results:     results,
		Complete:    commit.Complete,
	}, nil
}
