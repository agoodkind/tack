package service

import (
	"context"
	"crypto/rand"
	"log/slog"
	"math/big"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// sweepSessionLimit bounds the expired sessions one new search cleans up.
	sweepSessionLimit = 2
	// sweepCandidateWindow limits the oldest expired sessions eligible for a sweep.
	// Each process runs four searches at once by default. QA runs two processes.
	// Eight sweeps selecting two sessions each from 64 select about 14.4 distinct
	// sessions across 16 selections on average. One read loads about 4.3 KiB
	// of keys and does not commit. The backlog stops falling at about 32
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

// sweepExpired reads up to sweepCandidateWindow of the oldest expired sessions.
// It chooses up to sweepSessionLimit sessions at random and closes and deletes
// them. It logs storage failures and stops when the context ends. Later
// searches retry cleanup that did not finish.
func (s *SearchQueryService) sweepExpired(ctx context.Context) {
	expired, err := s.ports.Expired.ExpiredSessions(ctx, sweepCandidateWindow)
	if err != nil {
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.session.sweep_failed",
			slog.String("err", err.Error()),
		)

		return
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

			return
		}

		s.deleteSession(ctx, sessionID)
	}
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
