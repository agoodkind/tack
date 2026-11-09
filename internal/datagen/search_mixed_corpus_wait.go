package datagen

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
)

// searchMixedCorpusStall limits how long a corpus wait accepts an unchanged
// or increasing count of issues that search omits.
//
// A QA observation recorded these indexing measurements:
//
//   - Two search workers wrote about one page every 1.5 seconds.
//   - The setup corpus contained 222 issues.
//
// That historical rate allowed about 78 pages of other search work during the
// two-minute wait before a corpus group had to show progress.
const searchMixedCorpusStall = 2 * time.Minute

// These constants identify corpus groups in the order that setup waits for
// them.
const (
	searchMixedReadGroup        = "read"
	searchMixedForeignReadGroup = "foreign_read"
	searchMixedVictimGroup      = "victims"
)

// SearchMixedCorpusWait reports one setup wait for search to return every
// issue in a corpus group.
type SearchMixedCorpusWait struct {
	// Group is read, foreign_read, or victims.
	Group  string
	Tenant int
	Nodes  int
	Checks int
	// Unavailable counts unavailable responses that the wait's checks retried.
	Unavailable int
	// Elapsed measures time from the last setup create to the end of the first
	// check that returned every issue in the group. Setup waits for one group at a
	// time in wait order.
	Elapsed time.Duration
}

// searchMixedCorpusIndex collects one run's completed corpus waits. Only the
// setup goroutine writes it. [RunSearchMixed] reads it after setup returns.
type searchMixedCorpusIndex struct {
	waits []SearchMixedCorpusWait `exhaustruct:"optional"`
}

func (c *searchMixedCorpusIndex) record(wait SearchMixedCorpusWait) {
	c.waits = append(c.waits, wait)
}

func (c *searchMixedCorpusIndex) snapshot() []SearchMixedCorpusWait {
	return slices.Clone(c.waits)
}

// corpusCheck returns the corpus issues that a complete search traversal
// omits and the count of unavailable responses that it retried.
type corpusCheck func(context.Context) ([]uuid.UUID, int, error)

// corpusMissing returns a check that traverses every search page for word as
// run. The check returns omitted nodes from nodes and the count of unavailable
// responses that it retried. Each page uses retryUnavailable with a deadline
// of searchConvergenceDeadline after that page's first request starts. Retries
// stop when stopContext ends.
func corpusMissing(stopContext context.Context, run searchRun, word string, nodes []uuid.UUID) corpusCheck {
	identities := run.identities()
	return func(ctx context.Context) ([]uuid.UUID, int, error) {
		unavailable := 0
		results, err := traversePages(word, run.limits, func(cursor string) (searchPage, error) {
			page, _, err := retryUnavailable(stopContext, clock.Now().Add(searchConvergenceDeadline), &unavailable, func() (searchPage, error) {
				return callSearch(ctx, run.driver, identities, run.token, run.entry, word, cursor)
			})
			return page, err
		})
		if err != nil {
			return nil, unavailable, err
		}
		return omitted(results, nodes), unavailable, nil
	}
}

// awaitCorpus repeats check with callContext until search returns every node
// in nodes. It returns the check count and the number of unavailable responses
// that the checks retried.
//
// The wait fails if the omitted count does not decrease for
// searchMixedCorpusStall. A check that retried an unavailable response restarts
// the period because search did not report an omitted count during that
// response. An unavailable response at the retry deadline fails the wait
// immediately. If stopContext ends between checks or during unavailable
// response retries, awaitCorpus returns errSearchMixedSetupStopped.
func awaitCorpus(stopContext, callContext context.Context, description string, nodes []uuid.UUID, check corpusCheck) (int, int, error) {
	latest, fewest, progressed := nodes, len(nodes), clock.Now()
	unavailable := 0
	for checks := 1; ; checks++ {
		absent, repeated, err := check(callContext)
		unavailable += repeated
		if err == nil && len(absent) == 0 {
			return checks, unavailable, nil
		}
		if isSearchUnavailable(err) && stopContext.Err() != nil {
			return checks, unavailable, errSearchMixedSetupStopped
		}
		if isSearchUnavailable(err) {
			return checks, unavailable, loggedError(callContext, "qa datagen: "+description, err)
		}
		if repeated > 0 {
			progressed = clock.Now()
		}
		if err == nil {
			latest = absent
			if len(absent) < fewest {
				fewest, progressed = len(absent), clock.Now()
			}
		}
		if clock.Now().Sub(progressed) >= searchMixedCorpusStall {
			return checks, unavailable, corpusStalled(callContext, description, latest, len(nodes), err)
		}
		timer := time.NewTimer(searchPollInterval)
		select {
		case <-stopContext.Done():
			timer.Stop()
			return checks, unavailable, errSearchMixedSetupStopped
		case <-timer.C:
		}
	}
}

// corpusStalled reports the first node and the count of nodes that the latest
// successful check omitted. It adds checkErr when the last check failed.
func corpusStalled(ctx context.Context, description string, absent []uuid.UUID, total int, checkErr error) error {
	stalled := fmt.Errorf("search omitted %d of %d corpus nodes and the omitted count did not decrease for %s; the first missing node is %s",
		len(absent), total, searchMixedCorpusStall, absent[0])
	if checkErr != nil {
		stalled = errors.Join(stalled, checkErr)
	}
	return loggedError(ctx, "qa datagen: "+description, stalled)
}
