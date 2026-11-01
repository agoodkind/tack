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
// session ID exists before the point in time, because the audit target of the
// point in time states that ID.
func (s *SearchQueryService) open(ctx context.Context, request SearchRequest, authority uuid.UUID, query searchdomain.Query) (SearchPage, error) {
	index, err := s.ports.Index.ServingSearchIndex(ctx)
	if err != nil {
		return SearchPage{}, queryFailure(ctx, "read serving search index", uuid.Nil, err)
	}
	query.Index = index
	filter := query.Access
	session := searchdomain.Session{
		ID: uuid.Must(uuid.NewV7()), PrincipalID: request.PrincipalID, AuthorityID: authority,
		EntryPointID: request.EntryPointID,
		Binding:      searchdomain.SessionBinding(request.PrincipalID, authority, request.EntryPointID, query),
		Query:        query, Snapshot: searchdomain.Snapshot{PITID: "", Index: index, QueryTokens: nil},
		Sort: nil, Version: 0, CreatedAt: time.Time{}, IdleDeadline: time.Time{}, AbsoluteDeadline: time.Time{},
		Complete: false, Closing: false,
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
		return SearchPage{}, queryFailure(ctx, "create search session", session.ID, errors.Join(err, s.closeSnapshot(ctx, session, session.Snapshot)))
	}
	return s.advance(ctx, created, filter)
}

// openSnapshot creates the point in time of session under one audited step.
// It deletes a partial point in time under its own audited step. It returns
// [searchdomain.ErrEngineUnavailable] for an audit write failure after
// Ranker.Open succeeds or before it runs.
func (s *SearchQueryService) openSnapshot(ctx context.Context, session searchdomain.Session) (searchdomain.Snapshot, error) {
	var none, snapshot searchdomain.Snapshot
	var openErr error
	planned := snapshotEvidence{SessionID: session.ID, Index: session.Query.Index, PITID: ""}
	recordErr := s.runSnapshotStep(ctx, session, snapshotCreate, planned, func(ctx context.Context) (snapshotEvidence, error) {
		observed := planned
		snapshot, openErr = s.ports.Ranker.Open(ctx, session.Query)
		observed.PITID = snapshot.PITID
		if openErr != nil {
			return observed, snapshotStepError{err: openErr}
		}
		return observed, nil
	})
	if recordErr == nil {
		return snapshot, nil
	}
	if openErr == nil {
		slog.ErrorContext(ctx, "search.snapshot.step_failed", slog.String("err", recordErr.Error()), slog.String("session_id", session.ID.String()))
		return none, errors.Join(searchdomain.ErrEngineUnavailable, recordErr)
	}
	if errors.Is(openErr, searchdomain.ErrSnapshotPartial) {
		if closeErr := s.closeSnapshot(ctx, session, snapshot); closeErr != nil {
			telemetry.L(ctx).ErrorContext(ctx, "search.snapshot.close_failed", slog.String("err", closeErr.Error()), slog.String("session_id", session.ID.String()))
		}
	}
	return none, recordErr
}
