package datagen

import (
	"context"
	"time"

	"goodkind.io/tack/internal/clock"
)

// retryUnavailable runs search and returns the page and the start time of the request that the
// server answered. When search returns the unavailable response, retryUnavailable adds one to
// unavailable and runs the same search again after searchMixedPollInterval. It returns the
// unavailable error after searchConvergenceDeadline passes from the first request or when
// waitContext ends, and it returns every other error at once.
func retryUnavailable(waitContext context.Context, unavailable *int, search func() (searchPage, error)) (searchPage, time.Time, error) {
	deadline := clock.Now().Add(searchConvergenceDeadline)
	for {
		requested := clock.Now()
		page, err := search()
		if !isSearchUnavailable(err) {
			return page, requested, err
		}
		*unavailable++
		if clock.Now().After(deadline) || !waitForSearchMixedPoll(waitContext) {
			return searchPage{}, requested, err
		}
	}
}
