package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/runtime"
)

// SearchMixedOptions configures search load and concurrent issue writes.
type SearchMixedOptions struct {
	Seed        int64
	Rate        int
	WriteRate   int
	Duration    time.Duration
	Concurrency int
	Commit      bool
}

// SearchMixedSummary reports search results, write counts, freshness lag, and deletion verification.
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
}

// SearchMixedFreshness reports lag from a write commit to the first search that returns the issue.
type SearchMixedFreshness struct {
	P50 time.Duration
	P95 time.Duration
	P99 time.Duration
	Max time.Duration
}

type searchMixed struct {
	load      *searchLoad
	project   string
	runKey    string
	writeRate int
	pages     storedPageReader
	stats     *searchMixedStats
	probes    sync.WaitGroup     `exhaustruct:"optional"`
	nodeMutex sync.Mutex         `exhaustruct:"optional"`
	nodes     []*searchMixedNode `exhaustruct:"optional"`
	created   int                `exhaustruct:"optional"`
}

// RunSearchMixed measures search freshness during concurrent public issue writes.
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
		searches := (&searchLoadStats{}).summary(true, plannedSearches, 0, 0, 0, 0)
		return (&searchMixedStats{}).summary(searches, plannedWrites, 0), nil
	}
	if plannedSearches < 1 || plannedWrites < 1 {
		return SearchMixedSummary{}, fmt.Errorf("qa datagen search-mixed: schedule requires at least one search and one write: rate %d, write rate %d, duration %s", options.Rate, options.WriteRate, options.Duration)
	}
	if err := ValidateTarget(cfg); err != nil {
		return SearchMixedSummary{}, err
	}
	if !cfg.SearchPublicEnabled {
		return SearchMixedSummary{}, loggedError(callContext, "qa datagen search-mixed: public search must be enabled", errPublicSearchDisabled)
	}
	scale, err := ParseScale(searchVerificationScale)
	if err != nil {
		return SearchMixedSummary{}, err
	}
	graph, err := runtime.BuildGraph(callContext, cfg)
	if err != nil {
		return SearchMixedSummary{}, loggedError(callContext, "qa datagen search-mixed: build runtime", err)
	}
	defer graph.Close()
	identities, err := BootstrapIdentities(callContext, cfg, options.Seed, scale)
	if err != nil {
		return SearchMixedSummary{}, err
	}
	workspace := identities.Workspaces[0]
	queries, err := newSearchLoadQueries(callContext, options.Seed, workspace, plannedSearches)
	if err != nil {
		return SearchMixedSummary{}, err
	}
	load := &searchLoad{
		driver: NewDriver(graph, false, options.Seed), token: workspace.Actors[0].Token, entry: workspace.Slug,
		options: SearchLoadOptions{
			Seed: options.Seed, Rate: options.Rate, Duration: options.Duration,
			Concurrency: options.Concurrency, Commit: true,
		},
		queries: queries, stats: &searchLoadStats{},
	}
	mixed := &searchMixed{
		load: load, project: strings.ToUpper(opaqueKey("m")), runKey: opaqueKey("mix"),
		writeRate: options.WriteRate, pages: graph, stats: &searchMixedStats{},
	}
	if err := mixed.createProject(callContext); err != nil {
		return SearchMixedSummary{}, err
	}
	summary := mixed.run(scheduleContext, callContext, plannedSearches, plannedWrites)
	slog.InfoContext(callContext, "qa.datagen.search_mixed_finished", slog.Int("rate", options.Rate),
		slog.Int("write_rate", options.WriteRate), slog.Int("completed", summary.Search.Completed),
		slog.Int("creates", summary.Creates), slog.Int("edits", summary.Edits), slog.Int("deletes", summary.Deletes),
		slog.Int("markers_not_found", summary.MarkersNotFound), slog.Int("deleted_returned", summary.DeletedReturned))
	return summary, nil
}

func validateSearchMixedOptions(options SearchMixedOptions) error {
	if options.Duration <= 0 {
		return errors.New("qa datagen search-mixed: duration must be positive")
	}
	if options.Concurrency <= 0 {
		return errors.New("qa datagen search-mixed: concurrency must be positive")
	}
	if options.Rate < 0 || options.WriteRate < 0 {
		return errors.New("qa datagen search-mixed: rate and write rate must not be negative")
	}
	if options.Commit && (options.Rate == 0 || options.WriteRate == 0) {
		return errors.New("qa datagen search-mixed: rate and write rate must be positive with --commit")
	}
	return nil
}

func (m *searchMixed) createProject(ctx context.Context) error {
	properties := newProperties()
	properties.setString("identifier", m.project)
	_, err := m.load.driver.Call(ctx, m.load.token, "tack_create_project", ToolArguments{
		WorkspaceReference: m.load.entry, ProjectReference: "", IssueReference: "",
		Name: "Search mixed load " + m.runKey, Properties: properties, NodeID: "", Query: "", NodeType: "",
		Direction: "", SourceID: "", RelationType: "", TargetID: "",
	})
	if err != nil {
		return loggedError(ctx, "qa datagen search-mixed: create project", err)
	}
	return nil
}
