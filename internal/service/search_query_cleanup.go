package service

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// closeSession closes the point in time, marks the session closing, and
// reports whether BeginCleanup started a transaction attempt.
func (s *SearchQueryService) closeSession(
	ctx context.Context,
	session searchdomain.Session,
) bool {
	if err := s.closeSnapshot(ctx, session.Snapshot); err != nil {
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.snapshot.close_failed",
			slog.String("err", err.Error()),
			slog.String("session_id", session.ID.String()),
		)
	}

	if err := s.ports.Sessions.BeginCleanup(ctx, session.ID); err != nil {
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.session.close_failed",
			slog.String("err", err.Error()),
			slog.String("session_id", session.ID.String()),
		)

		return !errors.Is(err, searchdomain.ErrSessionStepNotStarted)
	}

	return true
}

// refuseExpired rejects expired continuations and starts session cleanup.
func (s *SearchQueryService) refuseExpired(
	ctx context.Context,
	session searchdomain.Session,
) error {
	if !session.Closing {
		s.closeSession(ctx, session)
	}

	return queryFailure(ctx, "verify search session", session.ID, searchdomain.ErrSessionExpired)
}

// deleteSession closes one expired session and deletes its records. A
// session that no longer exists counts as finished cleanup.
func (s *SearchQueryService) deleteSession(ctx context.Context, sessionID uuid.UUID) {
	session, err := s.ports.Sessions.Load(ctx, sessionID)
	if errors.Is(err, searchdomain.ErrSessionNotFound) {
		telemetry.L(ctx).DebugContext(
			ctx,
			"search.session.sweep_skipped",
			slog.String("session_id", sessionID.String()),
		)

		return
	}

	if err != nil {
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.session.sweep_failed",
			slog.String("err", err.Error()),
			slog.String("session_id", sessionID.String()),
		)

		return
	}

	if !session.Closing {
		if !s.closeSession(ctx, session) {
			return
		}
	}

	for slice := range sweepSliceLimit {
		done, err := s.ports.Sessions.CleanupSlice(ctx, sessionID, cleanupSliceKeys)
		if slice > 0 && errors.Is(err, searchdomain.ErrSessionStepNotStarted) {
			telemetry.L(ctx).InfoContext(
				ctx,
				"search.session.sweep_stopped",
				slog.String("session_id", sessionID.String()),
				slog.String("reason", err.Error()),
			)

			return
		}

		if err != nil {
			telemetry.L(ctx).ErrorContext(
				ctx,
				"search.session.sweep_failed",
				slog.String("err", err.Error()),
				slog.String("session_id", sessionID.String()),
			)

			return
		}

		if done {
			return
		}
	}
}
