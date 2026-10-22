package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/datagen"
)

type datagenSearchMixedInput struct {
	clispec.InputMarker
	Rate        int
	WriteRate   int
	Duration    string
	Concurrency int
	Seed        int
	Commit      bool
}

type datagenSearchMixedResult struct {
	datagenSearchLoadResult
	TargetWriteRatePerMinute int                         `json:"target_write_rate_per_minute"`
	PlannedWrites            int                         `json:"planned_writes"`
	Writes                   datagenSearchMixedWrites    `json:"writes"`
	WritesPerMinute          float64                     `json:"writes_per_minute"`
	FreshnessLagMs           datagenSearchMixedFreshness `json:"freshness_lag_ms"`
	MarkersNotFound          int                         `json:"markers_not_found"`
	DeletedNodesReturned     int                         `json:"deleted_nodes_returned"`
	DeletesUnverified        int                         `json:"deletes_unverified"`
}

type datagenSearchMixedWrites struct {
	Created int `json:"created"`
	Edited  int `json:"edited"`
	Deleted int `json:"deleted"`
	Failed  int `json:"failed"`
}

type datagenSearchMixedFreshness struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

func datagenSearchMixedOp(f *cli.Factory) clispec.Operation[datagenSearchMixedInput] {
	return clispec.Operation[datagenSearchMixedInput]{
		Name:     clispec.Name{Canonical: "search_mixed", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsDatagenSoak), Mutates: true},
		Group:    datagenGroup,
		Short:    "Measure search freshness during concurrent issue writes.",
		Long:     "The command sends first-page public tack_search calls at the offered rate. Public tool calls create, edit, and delete issues at the write rate. Each created or edited issue contains a unique marker word. The command searches for each marker to measure freshness lag from the write commit to the first search that returns the issue. The command fails if search returns a deleted issue after indexing processes the deletion. The command performs a dry run without --commit.",
		Params: []clispec.Param[datagenSearchMixedInput]{
			clispec.IntParam("rate", "Set the offered number of first-page public tack_search calls per minute.", 0, func(input *datagenSearchMixedInput, value int) { input.Rate = value }),
			clispec.IntParam("write-rate", "Set the number of issue writes per minute.", 0, func(input *datagenSearchMixedInput, value int) { input.WriteRate = value }),
			clispec.StringParam("duration", "Set the duration of the search and write schedules.", defaultDatagenSearchLoadDuration, false, func(input *datagenSearchMixedInput, value string) { input.Duration = value }),
			clispec.IntParam("concurrency", "Set the maximum number of concurrent load searches.", defaultDatagenSearchLoadConcurrency, func(input *datagenSearchMixedInput, value int) { input.Concurrency = value }),
			clispec.IntParam("seed", "Set the seed for generated identities and search queries.", defaultDatagenSeed, func(input *datagenSearchMixedInput, value int) { input.Seed = value }),
			clispec.BoolParam("commit", "Execute public searches and issue writes.", false, func(input *datagenSearchMixedInput, value bool) { input.Commit = value }),
		},
		New: func() datagenSearchMixedInput {
			return datagenSearchMixedInput{
				InputMarker: clispec.InputMarker{}, Rate: 0, WriteRate: 0, Duration: defaultDatagenSearchLoadDuration,
				Concurrency: defaultDatagenSearchLoadConcurrency, Seed: defaultDatagenSeed, Commit: false,
			}
		},
		Run: func(ctx context.Context, input datagenSearchMixedInput, sink clispec.ResultSink) error {
			return runDatagenSearchMixed(ctx, f, input, sink)
		},
	}
}

func runDatagenSearchMixed(ctx context.Context, factory *cli.Factory, input datagenSearchMixedInput, sink clispec.ResultSink) error {
	duration, err := time.ParseDuration(input.Duration)
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_duration_refused", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen search-mixed: parse duration %q: %w", input.Duration, err)
	}
	signalContext, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	callContext := context.WithoutCancel(signalContext)
	summary, err := datagen.RunSearchMixed(signalContext, callContext, factory.Cfg, datagen.SearchMixedOptions{
		Seed: int64(input.Seed), Rate: input.Rate, WriteRate: input.WriteRate, Duration: duration,
		Concurrency: input.Concurrency, Commit: input.Commit,
	})
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen search-mixed: run workload: %w", err)
	}
	if err := clispec.WriteJSONValue(ctx, sink, searchMixedResult(input, duration, summary)); err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_result_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen search-mixed: write result: %w", err)
	}
	if summary.DeletedReturned > 0 {
		deleted := fmt.Errorf("qa datagen search-mixed: search returned deleted issues after indexing processed the deletions: %d", summary.DeletedReturned)
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_deleted_nodes_returned", slog.String("err", deleted.Error()))
		return deleted
	}
	return nil
}

func searchMixedResult(input datagenSearchMixedInput, duration time.Duration, summary datagen.SearchMixedSummary) datagenSearchMixedResult {
	searches := summary.Search
	completedPerMinute := 0.0
	if searches.Elapsed > 0 {
		completedPerMinute = float64(searches.Completed) / searches.Elapsed.Minutes()
	}
	writesPerMinute := 0.0
	if summary.WriteElapsed > 0 {
		writesPerMinute = float64(summary.Creates+summary.Edits+summary.Deletes) / summary.WriteElapsed.Minutes()
	}
	return datagenSearchMixedResult{
		datagenSearchLoadResult: datagenSearchLoadResult{
			ResultMarker: clispec.ResultMarker{}, Command: "ops.qa.datagen.search_mixed",
			DryRun: searches.DryRun, Seed: int64(input.Seed), TargetRatePerMinute: input.Rate,
			Duration: duration.String(), Concurrency: input.Concurrency, Planned: searches.Planned,
			Sent: searches.Sent, Completed: searches.Completed, Dropped: searches.Dropped,
			DistinctQueryTexts: searches.DistinctQueries, CompletedPerMinute: completedPerMinute,
			Elapsed: searches.Elapsed.String(),
			Errors: datagenSearchLoadErrors{
				Unavailable: searches.Unavailable, ToolError: searches.ToolErrors, TransportError: searches.TransportErrors,
			},
			LatencyMs: datagenSearchLoadLatency{
				Min: searchLoadMilliseconds(searches.Latency.Min), P50: searchLoadMilliseconds(searches.Latency.P50),
				P95: searchLoadMilliseconds(searches.Latency.P95), P99: searchLoadMilliseconds(searches.Latency.P99),
				Max: searchLoadMilliseconds(searches.Latency.Max),
			},
		},
		TargetWriteRatePerMinute: input.WriteRate, PlannedWrites: summary.PlannedWrites,
		Writes: datagenSearchMixedWrites{
			Created: summary.Creates, Edited: summary.Edits, Deleted: summary.Deletes, Failed: summary.WriteErrors,
		},
		WritesPerMinute: writesPerMinute,
		FreshnessLagMs: datagenSearchMixedFreshness{
			P50: searchLoadMilliseconds(summary.Freshness.P50), P95: searchLoadMilliseconds(summary.Freshness.P95),
			P99: searchLoadMilliseconds(summary.Freshness.P99), Max: searchLoadMilliseconds(summary.Freshness.Max),
		},
		MarkersNotFound: summary.MarkersNotFound, DeletedNodesReturned: summary.DeletedReturned,
		DeletesUnverified: summary.DeletesUnverified,
	}
}
