package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/config"
)

// SearchMixedOptions configures search load, issue writes, and the workload phases.
type SearchMixedOptions struct {
	Seed        int64
	Rate        int
	WriteRate   int
	Duration    time.Duration
	Concurrency int
	Commit      bool
	BurstWrites int
	CatchUp     time.Duration
	Endpoints   []string
	Client      *http.Client
	Rebuild     SearchMixedRebuild
}

// SearchMixedRebuild starts one full index rebuild with the audited operation
// that ops search reindex --mode full --execute uses. It returns the target index.
type SearchMixedRebuild func(context.Context) (string, error)

// SearchMixedSummary reports the measurements and correctness checks of one workload run.
type SearchMixedSummary struct {
	Search            SearchLoadSummary
	PlannedWrites     int
	Creates           int
	Edits             int
	Deletes           int
	WriteErrors       int
	WriteElapsed      time.Duration
	Freshness         SearchMixedFreshness
	MarkersNotFound   int
	DeletedReturned   int
	DeletesUnverified int
	ProbeErrors       int
	Endpoints         int
	Tenants           int
	Burst             SearchMixedBurst
	Sessions          SearchMixedSessions
	Pending           SearchMixedPending
	Revocation        SearchMixedRevocation
	Rebuild           SearchMixedRebuildResult
	CatchUp           SearchMixedCatchUp
	Samples           []SearchMixedSample
	// Measured is true after the measured window ends, including when a workload phase fails.
	Measured bool
	// Stopped is true when the schedule context ended before setup or the probe drain finished.
	Stopped bool `exhaustruct:"optional"`
	// SkippedPhases lists the workload phases that did not start because the schedule context ended.
	SkippedPhases []string `exhaustruct:"optional"`
	// CorpusIndex lists the setup waits that finished before the measured window, in wait order.
	CorpusIndex []SearchMixedCorpusWait `exhaustruct:"optional"`
}

// SearchMixedFreshness reports the time between an issue's update time in FoundationDB and the
// first search that returns the issue for its marker word.
type SearchMixedFreshness struct {
	P50 time.Duration
	P95 time.Duration
	P99 time.Duration
	Max time.Duration
}

type searchMixed struct {
	cfg        *config.Config
	options    SearchMixedOptions
	drivers    []*Driver
	tenants    []*searchMixedTenant
	loads      []*searchLoad
	loadStats  *searchLoadStats
	runKey     string
	readWord   string
	victimWord string
	limits     searchLimits
	stores     *fdbadapter.Stores
	pages      storedPageReader
	backlog    *searchMixedBacklog
	resources  *searchMixedResources
	members    *postgres.OrgMemberRepo
	stats      *searchMixedStats
	phases     *searchMixedPhaseStats
	corpusIndex *searchMixedCorpusIndex
	corpus     searchMixedCorpus  `exhaustruct:"optional"`
	failureMutex sync.Mutex          `exhaustruct:"optional"`
	failures   []error               `exhaustruct:"optional"`
	probeMutex sync.Mutex            `exhaustruct:"optional"`
	markers    []searchMixedMarker   `exhaustruct:"optional"`
	deletions  []searchMixedDeletion `exhaustruct:"optional"`
	nodeMutex  sync.Mutex         `exhaustruct:"optional"`
	nodes      []*searchMixedNode `exhaustruct:"optional"`
	deleted    map[uuid.UUID]time.Time `exhaustruct:"optional"`
	created    int                `exhaustruct:"optional"`
}

// RunSearchMixed measures search during concurrent public issue writes and the workload phases.
// When scheduleContext ends while setup waits for search to return the corpus, RunSearchMixed
// returns an unmeasured summary with Stopped set and a nil error.
func RunSearchMixed(
	scheduleContext context.Context,
	callContext context.Context,
	cfg *config.Config,
	options SearchMixedOptions,
) (SearchMixedSummary, error) {
	if err := validateSearchMixedOptions(options); err != nil {
		return SearchMixedSummary{}, err
	}
	plannedSearches := int(options.Duration * time.Duration(options.Rate) / time.Minute)
	plannedWrites := int(options.Duration * time.Duration(options.WriteRate) / time.Minute)
	if !options.Commit {
		return unmeasuredSearchMixedSummary(options, true, plannedSearches, plannedWrites), nil
	}
	if plannedSearches < 1 || plannedWrites < 1 {
		return SearchMixedSummary{}, fmt.Errorf("qa datagen search-mixed: schedule requires at least one search and one write: rate %d, write rate %d, duration %s", options.Rate, options.WriteRate, options.Duration)
	}
	if err := ValidateTarget(cfg); err != nil {
		return SearchMixedSummary{}, err
	}
	if !cfg.SearchPublicEnabled {
		return SearchMixedSummary{}, loggedError(callContext, "qa datagen search-mixed: this command requires public search", errPublicSearchDisabled)
	}
	corpusIndex := &searchMixedCorpusIndex{}
	mixed, closeMixed, err := newSearchMixed(scheduleContext, callContext, cfg, options, plannedSearches, corpusIndex)
	if errors.Is(err, errSearchMixedSetupStopped) {
		slog.InfoContext(callContext, "qa.datagen.search_mixed_setup_stopped",
			slog.String("cause", context.Cause(scheduleContext).Error()))
		summary := unmeasuredSearchMixedSummary(options, false, plannedSearches, plannedWrites)
		summary.Stopped, summary.SkippedPhases = true, searchMixedPhaseNames(options.Rebuild != nil)
		summary.CorpusIndex = corpusIndex.snapshot()
		return summary, nil
	}
	if err != nil {
		return SearchMixedSummary{}, err
	}
	defer closeMixed()
	summary, err := mixed.run(scheduleContext, callContext, plannedWrites)
	summary.CorpusIndex = corpusIndex.snapshot()
	if err != nil {
		return summary, err
	}
	slog.InfoContext(callContext, "qa.datagen.search_mixed_finished", slog.Int("rate", options.Rate),
		slog.Int("write_rate", options.WriteRate), slog.Int("completed", summary.Search.Completed),
		slog.Int("creates", summary.Creates), slog.Int("edits", summary.Edits), slog.Int("deletes", summary.Deletes),
		slog.Int("burst_created", summary.Burst.Created), slog.Int("markers_not_found", summary.MarkersNotFound),
		slog.Int("deleted_returned", summary.DeletedReturned), slog.Bool("caught_up", summary.CatchUp.Completed))
	return summary, nil
}

// unmeasuredSearchMixedSummary returns the planned counts of a run that did not start the
// measured window.
func unmeasuredSearchMixedSummary(options SearchMixedOptions, dryRun bool, plannedSearches, plannedWrites int) SearchMixedSummary {
	searches := (&searchLoadStats{}).summary(dryRun, plannedSearches, 0, 0, 0, 0)
	summary := (&searchMixedStats{}).summary(searches, plannedWrites, 0)
	summary.Endpoints, summary.Burst.Planned = len(options.Endpoints), options.BurstWrites
	return summary
}

func validateSearchMixedOptions(options SearchMixedOptions) error {
	if options.Duration <= 0 {
		return errors.New("qa datagen search-mixed: duration must be positive")
	}
	if options.Concurrency <= 0 {
		return errors.New("qa datagen search-mixed: concurrency must be positive")
	}
	if options.Rate < 0 || options.WriteRate < 0 || options.BurstWrites < 0 {
		return errors.New("qa datagen search-mixed: rate, write rate, and burst writes must not be negative")
	}
	if options.CatchUp <= 0 {
		return errors.New("qa datagen search-mixed: catch-up timeout must be positive")
	}
	if options.Commit && (options.Rate == 0 || options.WriteRate == 0 || options.BurstWrites == 0) {
		return errors.New("qa datagen search-mixed: rate, write rate, and burst writes must be positive with --commit")
	}
	if len(options.Endpoints) > 0 && options.Client == nil {
		return errors.New("qa datagen search-mixed: endpoints require an HTTP client")
	}
	return nil
}
