package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// open creates the session of a new search and returns its first page. The
// session ID identifies the stored query state.
func (s *SearchQueryService) open(
	ctx context.Context,
	request SearchRequest,
	authority uuid.UUID,
	query searchdomain.Query,
) (SearchPage, error) {
	index, err := s.ports.Index.ServingSearchIndex(ctx)
	if err != nil {
		return SearchPage{}, queryFailure(ctx, "read serving search index", uuid.Nil, err)
	}

	query.Index = index
	filter := query.Access
	session := searchdomain.Session{
		ID:           uuid.Must(uuid.NewV7()),
		PrincipalID:  request.PrincipalID,
		AuthorityID:  authority,
		EntryPointID: request.EntryPointID,
		Binding: searchdomain.SessionBinding(
			request.PrincipalID,
			authority,
			request.EntryPointID,
			query,
		),
		Query:            query,
		Snapshot:         searchdomain.Snapshot{PITID: "", Index: index, QueryTokens: nil},
		Sort:             nil,
		Version:          0,
		CreatedAt:        time.Time{},
		IdleDeadline:     time.Time{},
		AbsoluteDeadline: time.Time{},
		Complete:         false,
		Closing:          false,
	}
	session.Snapshot, err = s.openSnapshot(ctx, session)
	if err != nil {
		return SearchPage{}, queryFailure(ctx, "open search snapshot", session.ID, err)
	}

	now := s.clock.Now().UTC()
	session.CreatedAt, session.IdleDeadline = now, now.Add(s.settings.IdleTimeout)
	session.AbsoluteDeadline = now.Add(s.settings.AbsoluteTimeout)
	created, err := s.ports.Sessions.Create(ctx, session)
	if err != nil {
		return SearchPage{}, queryFailure(
			ctx,
			"create search session",
			session.ID,
			errors.Join(err, s.closeSnapshot(ctx, session.Snapshot)),
		)
	}

	return s.advance(ctx, created, filter)
}

func (s *SearchQueryService) openSnapshot(
	ctx context.Context,
	session searchdomain.Session,
) (searchdomain.Snapshot, error) {
	snapshot, err := s.ports.Ranker.Open(ctx, session.Query)
	if err == nil {
		return snapshot, nil
	}

	if errors.Is(err, searchdomain.ErrSnapshotPartial) {
		if closeErr := s.closeSnapshot(ctx, snapshot); closeErr != nil {
			telemetry.L(ctx).ErrorContext(
				ctx,
				"search.snapshot.close_failed",
				slog.String("err", closeErr.Error()),
				slog.String("session_id", session.ID.String()),
			)
		}
	}

	return searchdomain.Snapshot{}, queryFailure(ctx, "open search snapshot", session.ID, err)
}
