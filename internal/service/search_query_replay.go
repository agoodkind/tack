package service

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
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
// result IDs. It rereads current summaries and permission data through the
// same batch method. It omits each committed node that is deleted, is
// currently forbidden, or no longer fits the response budget. It neither
// advances nor renews the session.
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

// closeSession closes the point in time and marks the session closing. The
// expiry sweep deletes its records.
func (s *SearchQueryService) closeSession(ctx context.Context, session searchdomain.Session) {
	if err := s.ports.Ranker.Close(ctx, session.Snapshot); err != nil {
		telemetry.L(ctx).ErrorContext(ctx, "search.snapshot.close_failed", slog.String("err", err.Error()), slog.String("session_id", session.ID.String()))
	}
	if err := s.ports.Sessions.BeginCleanup(ctx, session.ID); err != nil {
		telemetry.L(ctx).ErrorContext(ctx, "search.session.close_failed", slog.String("err", err.Error()), slog.String("session_id", session.ID.String()))
	}
}

// sweepExpired closes and deletes a bounded number of expired sessions. The
// public response does not depend on the sweep, and each failure is logged.
func (s *SearchQueryService) sweepExpired(ctx context.Context) {
	expired, err := s.ports.Expired.ExpiredSessions(ctx, sweepSessionLimit)
	if err != nil {
		telemetry.L(ctx).ErrorContext(ctx, "search.session.sweep_failed", slog.String("err", err.Error()))
		return
	}
	for _, sessionID := range expired {
		s.deleteSession(ctx, sessionID)
	}
}

func (s *SearchQueryService) deleteSession(ctx context.Context, sessionID uuid.UUID) {
	session, err := s.ports.Sessions.Load(ctx, sessionID)
	if err != nil {
		telemetry.L(ctx).ErrorContext(ctx, "search.session.sweep_failed", slog.String("err", err.Error()), slog.String("session_id", sessionID.String()))
		return
	}
	if !session.Closing {
		s.closeSession(ctx, session)
	}
	for range sweepSliceLimit {
		done, err := s.ports.Sessions.CleanupSlice(ctx, sessionID, cleanupSliceKeys)
		if err != nil {
			telemetry.L(ctx).ErrorContext(ctx, "search.session.sweep_failed", slog.String("err", err.Error()), slog.String("session_id", sessionID.String()))
			return
		}
		if done {
			return
		}
	}
}
