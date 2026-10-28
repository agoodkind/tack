package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/org"
)

const (
	revokedCursorContinuation = "continuation"
	revokedCursorReplay       = "replay"
	revokedCursorNewSearch    = "new search"
	// revokedRoundCalls is the largest number of calls in one revocation round.
	revokedRoundCalls = 3
)

// revokedSearchOutcome is the server's answer to one search call after membership removal.
type revokedSearchOutcome int

const (
	// revokedSearchAnswered means the server returned a page.
	revokedSearchAnswered revokedSearchOutcome = iota
	// revokedSearchRefused means the server returned an authorization refusal.
	revokedSearchRefused
	// revokedSearchUnavailable means the server returned the unavailable response.
	revokedSearchUnavailable
)

var errSearchMixedNoContinuation = errors.New("qa datagen search-mixed: the member session did not return a continuation cursor")

// checkRevocation opens a read word session as a member of the first tenant and reads two pages.
// It removes the membership and retries the continuation, second page replay, and new search
// to check whether the server refuses them. It then restores the membership with its original role.
func (m *searchMixed) checkRevocation(ctx context.Context) error {
	workspace := m.tenants[0].workspace
	actor := workspace.Actors[revokedActorIndex]
	session := m.sessionFor(0, revokedActorIndex, m.readWord, 0)
	first, second, err := m.openRevokedSession(ctx, session)
	if err != nil {
		return err
	}
	members := m.members
	member, err := findOrgMember(ctx, members, workspace.OrgID, actor.UserID)
	if err != nil {
		return err
	}
	if err := members.RemoveMember(ctx, workspace.OrgID, actor.UserID); err != nil {
		return loggedError(ctx, "qa datagen search-mixed: the command could not remove the organization membership for user "+actor.UserID.String(), err)
	}
	cursors := revokedCursors{continuation: second.Cursor, replay: first.Cursor}
	result, checkErr := m.awaitRefusal(ctx, session, cursors, clock.Now())
	m.phases.update(func(phases *searchMixedPhaseStats) {
		result.Unavailable += phases.revocation.Unavailable
		phases.revocation = result
	})
	restored := &org.Member{ID: uuid.Nil, OrgID: member.OrgID, UserID: member.UserID, Role: member.Role, CreatedAt: time.Time{}}
	if err := members.AddMember(context.WithoutCancel(ctx), restored); err != nil {
		joined := errors.Join(checkErr, fmt.Errorf("qa datagen search-mixed: the command could not restore the organization membership for user %s: %w", actor.UserID, err))
		slog.ErrorContext(ctx, "qa.datagen.member_restore_failed", slog.String("user_id", actor.UserID.String()),
			slog.String("err", joined.Error()))
		return joined
	}
	return checkErr
}

// revokedCursors stores the session cursors of the revocation check. continuation is the latest
// cursor that the session returned, and it is empty after the session completes. replay is the
// cursor that produced the latest committed page, because the session keeps a replay record for
// that page only.
type revokedCursors struct {
	continuation string
	replay       string
}

// revokedCall is one search of a revocation round.
type revokedCall struct {
	kind   string
	cursor string
}

// kinds returns the continuation, the replay, and the new search of one round. It omits the
// continuation after the session completes.
func (c revokedCursors) kinds() []string {
	kinds := make([]string, 0, revokedRoundCalls)
	if c.continuation != "" {
		kinds = append(kinds, revokedCursorContinuation)
	}
	return append(kinds, revokedCursorReplay, revokedCursorNewSearch)
}

// call returns the search of kind with its current cursor. A new search has no cursor.
func (c revokedCursors) call(kind string) revokedCall {
	cursor := ""
	if kind == revokedCursorContinuation {
		cursor = c.continuation
	}
	if kind == revokedCursorReplay {
		cursor = c.replay
	}
	return revokedCall{kind: kind, cursor: cursor}
}

// awaitRefusal retries the continuation, the replay, and a new search until the server refuses
// every call of a round or the time since membership removal exceeds the cache lifetime plus the
// convergence deadline. It allows pages from requests that start within the cache lifetime after
// membership removal. It counts issues from requests that start after that lifetime. An
// unavailable call counts as unavailable, not as a page, and keeps the round from counting as
// refused. An answered continuation advances the session. Its cursor becomes the replay cursor
// for the next call, including the replay of the same round, and the cursor it returned becomes
// the continuation cursor.
func (m *searchMixed) awaitRefusal(ctx context.Context, session searchMixedSession, cursors revokedCursors, removed time.Time) (SearchMixedRevocation, error) {
	lifetime := m.cfg.AuthMembershipCacheLifetime
	deadline := removed.Add(lifetime + searchConvergenceDeadline)
	result := SearchMixedRevocation{
		Checks: 0, PagesWithinCacheLifetime: 0, ResultsAfterCacheLifetime: 0, Unavailable: 0, RefusedAfter: 0, Refused: false,
	}
	for attempt := 0; ; attempt++ {
		refusedAll := true
		for position, kind := range cursors.kinds() {
			call := cursors.call(kind)
			started := clock.Now()
			outcome, page, err := m.revokedSearch(ctx, session, attempt+position, position, call)
			if err != nil {
				return result, err
			}
			result.Checks++
			switch outcome {
			case revokedSearchRefused:
			case revokedSearchUnavailable:
				refusedAll = false
				result.Unavailable++
			case revokedSearchAnswered:
				refusedAll = false
				if started.Sub(removed) <= lifetime {
					result.PagesWithinCacheLifetime++
				} else {
					result.ResultsAfterCacheLifetime += len(page.IDs)
				}
				if call.kind == revokedCursorContinuation {
					cursors = revokedCursors{continuation: page.Cursor, replay: call.cursor}
				}
			}
		}
		if refusedAll {
			result.Refused, result.RefusedAfter = true, clock.Now().Sub(removed)
			return result, nil
		}
		if clock.Now().After(deadline) {
			return result, nil
		}
		if !waitForSearchMixedPoll(ctx) {
			return result, loggedError(ctx, "qa datagen search-mixed: the command could not verify search refusal after membership removal", ctx.Err())
		}
	}
}

// revokedSearch uses driver number to request call from session and resolve
// results for its tenant. It classifies the response as a page, an authorization
// refusal, or search unavailability. It reports every other error with the
// call's position in the round.
func (m *searchMixed) revokedSearch(ctx context.Context, session searchMixedSession, number, position int, call revokedCall) (revokedSearchOutcome, searchPage, error) {
	driver := m.driver(number)
	page, err := callSearch(ctx, driver, m.identities(session.tenant, driver), session.token, session.entry, session.query, call.cursor)
	if err == nil {
		return revokedSearchAnswered, page, nil
	}
	if isAuthorizationRefusal(err, session.entry) {
		return revokedSearchRefused, searchPage{}, nil
	}
	if isSearchUnavailable(err) {
		return revokedSearchUnavailable, searchPage{}, nil
	}
	slog.ErrorContext(ctx, "qa.datagen.revoked_search_failed", slog.String("entry", session.entry),
		slog.Int("cursor_position", position), slog.String("cursor_kind", call.kind), slog.String("err", err.Error()))
	return revokedSearchAnswered, searchPage{}, fmt.Errorf("qa datagen search-mixed: the %s search at cursor position %d under %s failed without an authorization refusal: %w",
		call.kind, position, session.entry, err)
}
