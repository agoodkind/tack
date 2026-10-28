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

// answeredPage requests page number of session with cursor using driver session.offset+number
// and resolves the results for the session tenant. It returns the page and the start time
// of the request the server answered. With an empty cursor, it repeats unavailable requests
// for up to searchConvergenceDeadline from the first request. For a continued page, retries
// start no later than tally.answered plus m.sessionIdle. If that page stays unavailable at
// the limit and ctx has not ended, it increments tally.expiredDuringOutage and returns an
// error wrapping errSearchMixedSessionExpired and the unavailable error. An answered request
// updates tally.answered to its start time.
func (m *searchMixed) answeredPage(ctx context.Context, session searchMixedSession, number int, cursor string, tally *searchMixedTally) (searchPage, time.Time, error) {
	driver := m.driver(session.offset + number)
	identities := m.identities(session.tenant, driver)
	latestStart := convergenceRetryEnd()
	if cursor != "" {
		latestStart = tally.answered.Add(m.sessionIdle)
	}
	page, requested, err := retryUnavailable(ctx, latestStart, &tally.unavailable, func() (searchPage, error) {
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

// convergenceRetryEnd returns now plus searchConvergenceDeadline and searchMixedPollInterval.
// The extra interval lets retryUnavailable repeat a request while searchConvergenceDeadline
// has not passed since the first request.
func convergenceRetryEnd() time.Time {
	return clock.Now().Add(searchConvergenceDeadline + searchMixedPollInterval)
}

// retryUnavailable runs search and returns every result other than the unavailable response
// at once. After each unavailable response, it increments *unavailable. It repeats search
// after searchMixedPollInterval only if the next request would start no later than latestStart
// and waitContext has not ended. Otherwise, it returns the unavailable error. On an answered
// request, it returns the page and that request's start time.
func retryUnavailable(waitContext context.Context, latestStart time.Time, unavailable *int, search func() (searchPage, error)) (searchPage, time.Time, error) {
	for {
		requested := clock.Now()
		page, err := search()
		if !isSearchUnavailable(err) {
			return page, requested, err
		}
		*unavailable++
		if clock.Now().Add(searchMixedPollInterval).After(latestStart) || !waitForSearchMixedPoll(waitContext) {
			return searchPage{}, requested, err
		}
	}
}
