package service

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// sweepSessionLimit bounds the expired sessions one new search cleans up.
	sweepSessionLimit = 4
	// sweepSliceLimit bounds the cleanup slices one expired session receives
	// per sweep.
	sweepSliceLimit = 4
	// cleanupSliceKeys bounds the child records one cleanup slice deletes.
	cleanupSliceKeys = 100
)

// replay rebuilds the page of the cursor at version from its committed
// result IDs. It rereads current summaries and access keys with the same
// summary reader that page reads use. It omits each committed node that is
// deleted, is currently forbidden, or no longer fits the response budget. It
// neither advances nor renews the session.
func (s *SearchQueryService) replay(ctx context.Context, session searchdomain.Session, version uint64, filter searchdomain.AccessFilter) (SearchPage, error) {
	commit, found, err := s.ports.Sessions.Replay(ctx, session.ID, version)
	if err != nil {
		return SearchPage{}, queryFailure(ctx, "read replay", session.ID, err)
	}
	if !found {
		return SearchPage{}, queryFailure(ctx, "read replay", session.ID, searchdomain.ErrSessionChanged)
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
	telemetry.L(ctx).InfoContext(ctx, "search.page.replayed", slog.String("session_id", session.ID.String()),
		slog.Uint64("version", version), slog.Int("results", len(results)), slog.Int("withheld", len(commit.ResultIDs)-len(results)))
	return SearchPage{SessionID: session.ID, NextVersion: version + 1, Results: results, Complete: commit.Complete}, nil
}

// closeSession closes the point in time and marks the session closing. It
// returns an error if an audit event for the close or cleanup failure cannot
// be recorded. This includes a failed step result write when the operation
// result records step_unfinished. It logs other point in time close errors.
// The expiry sweep deletes the session records.
func (s *SearchQueryService) closeSession(ctx context.Context, session searchdomain.Session) error {
	var snapshotRecordErr error
	if err := s.closeSnapshot(ctx, session, session.Snapshot); err != nil {
		telemetry.L(ctx).ErrorContext(ctx, "search.snapshot.close_failed", slog.String("err", err.Error()), slog.String("session_id", session.ID.String()))
		if errors.Is(err, audit.ErrEventNotRecorded) {
			snapshotRecordErr = errors.Join(searchdomain.ErrEngineUnavailable, err)
		}
	}
	if err := s.ports.Sessions.BeginCleanup(ctx, session.ID); err != nil {
		telemetry.L(ctx).ErrorContext(ctx, "search.session.close_failed", slog.String("err", err.Error()), slog.String("session_id", session.ID.String()))
		failure := errors.Join(snapshotRecordErr, s.recordCleanupFailure(ctx, session.ID, searchdomain.CleanupClose, err))
		return failure
	}
	return snapshotRecordErr
}

// refuseExpired rejects continuations when a session is closing or has passed
// an idle or absolute deadline. Unless the session is closing, it closes the
// point in time and marks the session closing. It returns
// searchdomain.ErrSessionExpired joined with any closeSession error. The
// expiry sweep later deletes the session records.
func (s *SearchQueryService) refuseExpired(ctx context.Context, session searchdomain.Session) error {
	var closeErr error
	if !session.Closing {
		closeErr = s.closeSession(ctx, session)
	}
	refusal := errors.Join(queryFailure(ctx, "verify search session", session.ID, searchdomain.ErrSessionExpired), closeErr)
	return refusal
}

// sweepExpired closes and deletes up to four expired sessions. It stops and
// returns the first error from deleteSession. If ExpiredSessions fails, it
// logs the error and returns nil without running cleanup.
func (s *SearchQueryService) sweepExpired(ctx context.Context) error {
	expired, err := s.ports.Expired.ExpiredSessions(ctx, sweepSessionLimit)
	if err != nil {
		telemetry.L(ctx).ErrorContext(ctx, "search.session.sweep_failed", slog.String("err", err.Error()))
		return nil
	}
	for _, sessionID := range expired {
		if err := s.deleteSession(ctx, sessionID); err != nil {
			return err
		}
	}
	return nil
}

func (s *SearchQueryService) deleteSession(ctx context.Context, sessionID uuid.UUID) error {
	session, err := s.ports.Sessions.Load(ctx, sessionID)
	if err != nil {
		telemetry.L(ctx).ErrorContext(ctx, "search.session.sweep_failed", slog.String("err", err.Error()), slog.String("session_id", sessionID.String()))
		// If the session does not exist, the sweep stops for that session and does not record
		// a cleanup failure.
		if !errors.Is(err, searchdomain.ErrSessionNotFound) {
			return s.recordCleanupFailure(ctx, sessionID, searchdomain.CleanupDelete, err)
		}
		return nil
	}
	if !session.Closing {
		if closeErr := s.closeSession(ctx, session); closeErr != nil {
			return closeErr
		}
	}
	for range sweepSliceLimit {
		done, err := s.ports.Sessions.CleanupSlice(ctx, sessionID, cleanupSliceKeys)
		if err != nil {
			telemetry.L(ctx).ErrorContext(ctx, "search.session.sweep_failed", slog.String("err", err.Error()), slog.String("session_id", sessionID.String()))
			return s.recordCleanupFailure(ctx, sessionID, searchdomain.CleanupDelete, err)
		}
		if done {
			return nil
		}
	}
	return nil
}
