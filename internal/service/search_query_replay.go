package service

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"math/big"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// sweepSessionLimit bounds the expired sessions one new search cleans up.
	sweepSessionLimit = 2
	// sweepCandidateWindow limits the oldest expired sessions eligible for a sweep.
	// Each process runs four searches at once by default. QA runs two processes.
	// Eight sweeps selecting two sessions each from 64 select about 14.4 distinct
	// sessions across 16 selections on average. One read loads about 4.3 KiB of keys
	// and does not commit. By calculation, the backlog stops falling at about 32
	// undeletable keys in a full window. A full window of 64 undeletable keys with
	// valid session IDs stops sweeping. One malformed key in the window stops all
	// sweeping because the read fails.
	sweepCandidateWindow = 64
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

// closeSession closes the point in time and marks the session closing. It
// returns an error if an audit event for the close or cleanup failure cannot
// be recorded. This includes a failed step result write when the operation
// result records step_unfinished. It logs other point in time close errors.
// The expiry sweep deletes the session records.
func (s *SearchQueryService) closeSession(ctx context.Context, session searchdomain.Session) error {
	_, err := s.closeSessionStep(ctx, session)
	return err
}

// closeSessionStep closes the point in time, marks the session closing, and
// reports whether BeginCleanup started a transaction attempt.
func (s *SearchQueryService) closeSessionStep(
	ctx context.Context,
	session searchdomain.Session,
) (bool, error) {
	var snapshotRecordErr error
	if err := s.closeSnapshot(ctx, session, session.Snapshot); err != nil {
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.snapshot.close_failed",
			slog.String("err", err.Error()),
			slog.String("session_id", session.ID.String()),
		)
		if errors.Is(err, audit.ErrEventNotRecorded) {
			snapshotRecordErr = errors.Join(searchdomain.ErrEngineUnavailable, err)
		}
	}

	if err := s.ports.Sessions.BeginCleanup(ctx, session.ID); err != nil {
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.session.close_failed",
			slog.String("err", err.Error()),
			slog.String("session_id", session.ID.String()),
		)
		failure := errors.Join(
			snapshotRecordErr,
			s.recordCleanupFailure(ctx, session.ID, searchdomain.CleanupClose, err),
		)

		return !errors.Is(err, searchdomain.ErrSessionStepNotStarted), failure
	}

	return true, snapshotRecordErr
}

// refuseExpired rejects continuations when a session is closing or has passed
// an idle or absolute deadline. Unless the session is closing, it closes the
// point in time and marks the session closing. It returns
// searchdomain.ErrSessionExpired joined with any closeSession error. The
// expiry sweep later deletes the session records.
func (s *SearchQueryService) refuseExpired(
	ctx context.Context,
	session searchdomain.Session,
) error {
	var closeErr error
	if !session.Closing {
		closeErr = s.closeSession(ctx, session)
	}

	refusal := errors.Join(
		queryFailure(ctx, "verify search session", session.ID, searchdomain.ErrSessionExpired),
		closeErr,
	)

	return refusal
}

// sweepExpired reads up to sweepCandidateWindow of the oldest expired sessions.
// It chooses up to sweepSessionLimit sessions at random and closes and deletes
// them. It stops and returns the first deleteSession error. If ExpiredSessions
// fails, it logs the error and returns nil without cleanup. If ctx ends, it
// returns nil before the next session. A later search sweeps that session.
func (s *SearchQueryService) sweepExpired(ctx context.Context) error {
	expired, err := s.ports.Expired.ExpiredSessions(ctx, sweepCandidateWindow)
	if err != nil {
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.session.sweep_failed",
			slog.String("err", err.Error()),
		)

		return nil
	}

	chosen := chooseSweepSessions(ctx, expired)
	for position, sessionID := range chosen {
		if err := ctx.Err(); err != nil {
			telemetry.L(ctx).InfoContext(
				ctx,
				"search.session.sweep_stopped",
				slog.Int("remaining", len(chosen)-position),
				slog.String("reason", err.Error()),
			)

			return nil
		}

		if err := s.deleteSession(ctx, sessionID); err != nil {
			return err
		}
	}

	return nil
}

func chooseSweepSessions(ctx context.Context, candidates []uuid.UUID) []uuid.UUID {
	chosen := min(sweepSessionLimit, len(candidates))
	for position := range chosen {
		offset, err := rand.Int(rand.Reader, big.NewInt(int64(len(candidates)-position)))
		if err != nil {
			telemetry.L(ctx).ErrorContext(
				ctx,
				"search.session.sweep_failed",
				slog.String("err", err.Error()),
			)

			return nil
		}

		pick := position + int(offset.Int64())
		candidates[position], candidates[pick] = candidates[pick], candidates[position]
	}

	return candidates[:chosen]
}

// deleteSession closes one expired session and deletes its records. A
// session that no longer exists counts as finished cleanup.
func (s *SearchQueryService) deleteSession(ctx context.Context, sessionID uuid.UUID) error {
	session, err := s.ports.Sessions.Load(ctx, sessionID)
	if errors.Is(err, searchdomain.ErrSessionNotFound) {
		telemetry.L(ctx).DebugContext(
			ctx,
			"search.session.sweep_skipped",
			slog.String("session_id", sessionID.String()),
		)

		return nil
	}

	if err != nil {
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.session.sweep_failed",
			slog.String("err", err.Error()),
			slog.String("session_id", sessionID.String()),
		)

		return s.recordCleanupFailure(ctx, sessionID, searchdomain.CleanupDelete, err)
	}

	if !session.Closing {
		started, closeErr := s.closeSessionStep(ctx, session)
		if closeErr != nil {
			return closeErr
		}

		if !started {
			// deleteSession skips CleanupSlice because the close row already
			// records that cleanup did not start.
			return nil
		}
	}

	for slice := range sweepSliceLimit {
		done, err := s.ports.Sessions.CleanupSlice(ctx, sessionID, cleanupSliceKeys)
		if slice > 0 && errors.Is(err, searchdomain.ErrSessionStepNotStarted) {
			// An earlier slice committed delete effects. Do not record a failed outcome
			// when this slice made no attempt after the context ended; a later sweep
			// continues the delete.
			telemetry.L(ctx).InfoContext(
				ctx,
				"search.session.sweep_stopped",
				slog.String("session_id", sessionID.String()),
				slog.String("reason", err.Error()),
			)

			return nil
		}

		if err != nil {
			telemetry.L(ctx).ErrorContext(
				ctx,
				"search.session.sweep_failed",
				slog.String("err", err.Error()),
				slog.String("session_id", sessionID.String()),
			)

			return s.recordCleanupFailure(ctx, sessionID, searchdomain.CleanupDelete, err)
		}

		if done {
			return nil
		}
	}

	return nil
}
