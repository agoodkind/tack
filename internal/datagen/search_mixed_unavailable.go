package datagen

import (
	"context"
	"errors"
	"time"

	"goodkind.io/tack/internal/clock"
)

// errSearchMixedSessionExpired reports that a continued page stayed unavailable until
// the session idle timeout prevented another retry.
var errSearchMixedSessionExpired = errors.New("qa datagen search-mixed: the search session expired while search was unavailable")

// answeredPage requests page number of session with cursor.
// First pages use a retry deadline of searchConvergenceDeadline after the first request starts.
// Continued pages use tally.answered plus m.sessionIdle as their retry deadline.
// After an answered request, it sets tally.answered to that request's start time, resolves
// results for the session tenant, and returns the page and that time. If a continued page still
// returns the unavailable response at its deadline and ctx has not ended, it increments
// tally.expiredDuringOutage and returns an error that wraps errSearchMixedSessionExpired and
// the unavailable error.
func (m *searchMixed) answeredPage(ctx context.Context, session searchMixedSession, number int, cursor string, tally *searchMixedTally) (searchPage, time.Time, error) {
	driver := m.driver(session.offset + number)
	identities := m.identities(session.tenant, driver)
	deadline := clock.Now().Add(searchConvergenceDeadline)
	if cursor != "" {
		deadline = tally.answered.Add(m.sessionIdle)
	}
	page, requested, err := retryUnavailable(ctx, deadline, &tally.unavailable, func() (searchPage, error) {
		return callSearch(ctx, driver, identities, session.token, session.entry, session.query, cursor)
	})
	if err == nil {
		tally.answered = requested
		return page, requested, nil
	}
	if cursor != "" && isSearchUnavailable(err) && ctx.Err() == nil {
		tally.expiredDuringOutage++
		expired := errors.Join(errSearchMixedSessionExpired, err)
		return searchPage{}, requested, loggedError(ctx, "qa datagen search-mixed: continue the session for "+session.query, expired)
	}
	return searchPage{}, requested, err
}

// retryUnavailable runs search and repeats the same request after the unavailable response.
// It counts each unavailable response in *unavailable and waits searchMixedPollInterval.
// It retries only while waitContext has not ended and the current time is before deadline.
// It returns any other result with that request's start time. When retries stop, it returns
// the last unavailable error with the last request's start time.
func retryUnavailable(waitContext context.Context, deadline time.Time, unavailable *int, search func() (searchPage, error)) (searchPage, time.Time, error) {
	for {
		requested := clock.Now()
		page, err := search()
		if !isSearchUnavailable(err) {
			return page, requested, err
		}
		*unavailable++
		if !waitForSearchMixedPoll(waitContext) || !clock.Now().Before(deadline) {
			return searchPage{}, requested, err
		}
	}
}
