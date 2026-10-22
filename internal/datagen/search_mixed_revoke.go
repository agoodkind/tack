package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/org"
)

var errSearchMixedNoContinuation = errors.New("qa datagen search-mixed: the member session did not return a continuation cursor")

// checkRevocation opens a read word session as a member of the first tenant and reads two pages.
// It removes the membership and retries the continuation, second page replay, and new search
// to check whether the server refuses them. It then restores the membership with its original role.
func (m *searchMixed) checkRevocation(ctx context.Context) error {
	workspace := m.tenants[0].workspace
	actor := workspace.Actors[revokedActorIndex]
	session := m.sessionFor(0, revokedActorIndex, m.readWord, 0)
	tally := searchMixedTally{}
	first, err := m.page(ctx, session, 0, "", &tally)
	if err != nil {
		return err
	}
	second, err := m.page(ctx, session, 1, first.Cursor, &tally)
	if err != nil {
		return err
	}
	if first.Cursor == "" || second.Cursor == "" {
		return loggedError(ctx, "qa datagen search-mixed: the command could not open the member's search session", errSearchMixedNoContinuation)
	}
	pool, err := postgres.NewPool(ctx, m.cfg.DatabaseURL, nil)
	if err != nil {
		return loggedError(ctx, "qa datagen search-mixed: the command could not connect to YugabyteDB for the revocation check", err)
	}
	defer pool.Close()
	members := postgres.NewOrgMemberRepo(pool)
	member, err := findOrgMember(ctx, members, workspace.OrgID, actor.UserID)
	if err != nil {
		return err
	}
	if err := members.RemoveMember(ctx, workspace.OrgID, actor.UserID); err != nil {
		return loggedError(ctx, "qa datagen search-mixed: the command could not remove the organization membership for user "+actor.UserID.String(), err)
	}
	result, checkErr := m.awaitRefusal(ctx, session, []string{second.Cursor, first.Cursor, ""}, clock.Now())
	m.phases.update(func(phases *searchMixedPhaseStats) { phases.revocation = result })
	restored := &org.Member{ID: uuid.Nil, OrgID: member.OrgID, UserID: member.UserID, Role: member.Role, CreatedAt: time.Time{}}
	if err := members.AddMember(context.WithoutCancel(ctx), restored); err != nil {
		joined := errors.Join(checkErr, fmt.Errorf("qa datagen search-mixed: the command could not restore the organization membership for user %s: %w", actor.UserID, err))
		slog.ErrorContext(ctx, "qa.datagen.member_restore_failed", slog.String("user_id", actor.UserID.String()),
			slog.String("err", joined.Error()))
		return joined
	}
	return checkErr
}

// awaitRefusal retries a search with each cursor until the server refuses every call or the
// time since membership removal exceeds the cache lifetime plus the convergence deadline.
// It allows pages from requests that start within the cache lifetime after membership removal.
// It counts issues from requests that start after that lifetime.
func (m *searchMixed) awaitRefusal(ctx context.Context, session searchMixedSession, cursors []string, removed time.Time) (SearchMixedRevocation, error) {
	lifetime := m.cfg.AuthMembershipCacheLifetime
	deadline := removed.Add(lifetime + searchConvergenceDeadline)
	result := SearchMixedRevocation{
		Checks: 0, PagesWithinCacheLifetime: 0, ResultsAfterCacheLifetime: 0, RefusedAfter: 0, Refused: false,
	}
	for attempt := 0; ; attempt++ {
		refusedAll := true
		for position, cursor := range cursors {
			started := clock.Now()
			refused, returned, err := m.revokedSearch(ctx, session, attempt+position, cursor)
			if err != nil {
				return result, err
			}
			result.Checks++
			if refused {
				continue
			}
			refusedAll = false
			if started.Sub(removed) <= lifetime {
				result.PagesWithinCacheLifetime++
			} else {
				result.ResultsAfterCacheLifetime += len(returned)
			}
		}
		if refusedAll {
			result.Refused, result.RefusedAfter = true, clock.Now().Sub(removed)
			return result, nil
		}
		if clock.Now().After(deadline) || !waitForSearchMixedPoll(ctx) {
			return result, nil
		}
	}
}

// revokedSearch uses driver number to request one page of the session.
// It reports whether the server refused the call and returns issue IDs when the call succeeds.
func (m *searchMixed) revokedSearch(ctx context.Context, session searchMixedSession, number int, cursor string) (bool, []uuid.UUID, error) {
	page, err := callSearch(ctx, m.driver(number), session.token, session.entry, session.query, cursor)
	if err == nil {
		return false, page.IDs, nil
	}
	if isAuthorizationRefusal(err, session.entry) {
		return true, nil, nil
	}
	run := m.searchRun(session.tenant, revokedActorIndex, number)
	if refusalErr := run.refused(ctx, session.token, session.entry, cursor); refusalErr != nil {
		return false, nil, refusalErr
	}
	return true, nil, nil
}
