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

// searchMixedCorpusStall is the longest time that a corpus wait accepts without a decrease in
// the number of corpus issues that search omits. On QA, two search workers wrote one page about
// every 1.5 seconds while they indexed the 222 setup issues. Two minutes lets about 78 pages of
// other search work finish before a corpus group must show progress.
const searchMixedCorpusStall = 2 * time.Minute

// Corpus groups that setup waits for, in wait order.
const (
	searchMixedReadGroup        = "read"
	searchMixedForeignReadGroup = "foreign_read"
	searchMixedVictimGroup      = "victims"
)

// SearchMixedCorpusWait reports one setup wait for search to return every issue of a corpus group.
type SearchMixedCorpusWait struct {
	// Group is read, foreign_read, or victims.
	Group  string
	Tenant int
	Nodes  int
	Checks int
	// Unavailable counts the unavailable responses that the checks of the wait repeated.
	Unavailable int
	// Elapsed is the time from the end of the last setup create to the end of the first check
	// that returned every issue of the group. Setup waits for one group at a time in wait order.
	Elapsed time.Duration
}

// searchMixedCorpusIndex collects the completed corpus waits of one run. Only the setup goroutine
// writes it, and RunSearchMixed reads it after setup returns.
type searchMixedCorpusIndex struct {
	waits []SearchMixedCorpusWait `exhaustruct:"optional"`
}

func (c *searchMixedCorpusIndex) record(wait SearchMixedCorpusWait) {
	c.waits = append(c.waits, wait)
}

func (c *searchMixedCorpusIndex) snapshot() []SearchMixedCorpusWait {
	return slices.Clone(c.waits)
}

// corpusCheck returns the corpus issues that one complete search traversal omits and the number
// of unavailable responses that the traversal repeated.
type corpusCheck func(context.Context) ([]uuid.UUID, int, error)

// corpusMissing returns a corpusCheck for the nodes that the traversal of run for word omits.
// retryUnavailable repeats each page request that returns the unavailable response until
// stopContext ends.
func corpusMissing(stopContext context.Context, run searchRun, word string, nodes []uuid.UUID) corpusCheck {
	identities := run.identities()
	return func(ctx context.Context) ([]uuid.UUID, int, error) {
		unavailable := 0
		results, err := traversePages(word, run.limits, func(cursor string) (searchPage, error) {
			page, _, err := retryUnavailable(stopContext, &unavailable, func() (searchPage, error) {
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

// awaitCorpus repeats check with callContext until search returns every node in nodes, and
// returns the number of checks and the unavailable responses that the checks repeated. It fails
// when the number of omitted nodes does not decrease for searchMixedCorpusStall. A check that
// repeated an unavailable response restarts that period, because search did not report an
// omitted count while it was unavailable. awaitCorpus fails at once when a check returns the
// unavailable response at its retry deadline. It returns errSearchMixedSetupStopped when
// stopContext ends between checks or while a check repeats an unavailable response.
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

// corpusStalled reports the first node and the number of nodes that the latest successful check
// omitted. It adds checkErr when the last check failed.
func corpusStalled(ctx context.Context, description string, absent []uuid.UUID, total int, checkErr error) error {
	stalled := fmt.Errorf("search omitted %d of %d corpus nodes and the omitted count did not decrease for %s; the first missing node is %s",
		len(absent), total, searchMixedCorpusStall, absent[0])
	if checkErr != nil {
		stalled = errors.Join(stalled, checkErr)
	}
	return loggedError(ctx, "qa datagen: "+description, stalled)
}
