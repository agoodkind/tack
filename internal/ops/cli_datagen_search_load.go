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

const (
	defaultDatagenSearchLoadDuration    = "60s"
	defaultDatagenSearchLoadConcurrency = 64
)

type datagenSearchLoadInput struct {
	clispec.InputMarker
	Rate        int
	Duration    string
	Concurrency int
	Seed        int
	Commit      bool
}

type datagenSearchLoadResult struct {
	clispec.ResultMarker
	Command             string                   `json:"command"`
	DryRun              bool                     `json:"dry_run"`
	Seed                int64                    `json:"seed"`
	TargetRatePerMinute int                      `json:"target_rate_per_minute"`
	Duration            string                   `json:"duration"`
	Concurrency         int                      `json:"concurrency"`
	Planned             int                      `json:"planned"`
	Sent                int                      `json:"sent"`
	Completed           int                      `json:"completed"`
	Dropped             int                      `json:"dropped"`
	DistinctQueryTexts  int                      `json:"distinct_query_texts"`
	CompletedPerMinute  float64                  `json:"completed_per_minute"`
	Elapsed             string                   `json:"elapsed"`
	Errors              datagenSearchLoadErrors  `json:"errors"`
	LatencyMs           datagenSearchLoadLatency `json:"latency_ms"`
}

type datagenSearchLoadErrors struct {
	Unavailable    int `json:"unavailable"`
	ToolError      int `json:"tool_error"`
	TransportError int `json:"transport_error"`
}

type datagenSearchLoadLatency struct {
	Min float64 `json:"min"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

func datagenSearchLoadOp(f *cli.Factory) clispec.Operation[datagenSearchLoadInput] {
	return clispec.Operation[datagenSearchLoadInput]{
		Name:     clispec.Name{Canonical: "search_load", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsDatagenSoak), Mutates: true},
		Group:    datagenGroup,
		Short:    "Send first-page tack_search requests at a target rate.",
		Long:     "The command sends first-page public tack_search calls with distinct query texts at a target rate in requests per minute. The command counts a scheduled request as dropped without sending it when requests in flight equal --concurrency. The command performs a dry run without --commit. The result reports sent, completed, and dropped requests, completed requests per minute, errors by class, and latency percentiles.",
		Params: []clispec.Param[datagenSearchLoadInput]{
			clispec.IntParam("rate", "Set the target rate in requests per minute.", 0, func(input *datagenSearchLoadInput, value int) { input.Rate = value }),
			clispec.StringParam("duration", "Set the request scheduling duration.", defaultDatagenSearchLoadDuration, false, func(input *datagenSearchLoadInput, value string) { input.Duration = value }),
			clispec.IntParam("concurrency", "Set the maximum number of requests in flight.", defaultDatagenSearchLoadConcurrency, func(input *datagenSearchLoadInput, value int) { input.Concurrency = value }),
			clispec.IntParam("seed", "Set the random seed.", defaultDatagenSeed, func(input *datagenSearchLoadInput, value int) { input.Seed = value }),
			clispec.BoolParam("commit", "Execute the search workload.", false, func(input *datagenSearchLoadInput, value bool) { input.Commit = value }),
		},
		New: func() datagenSearchLoadInput {
			return datagenSearchLoadInput{
				InputMarker: clispec.InputMarker{}, Rate: 0, Duration: defaultDatagenSearchLoadDuration,
				Concurrency: defaultDatagenSearchLoadConcurrency, Seed: defaultDatagenSeed, Commit: false,
			}
		},
		Run: func(ctx context.Context, input datagenSearchLoadInput, sink clispec.ResultSink) error {
			return runDatagenSearchLoad(ctx, f, input, sink)
		},
	}
}

func runDatagenSearchLoad(ctx context.Context, factory *cli.Factory, input datagenSearchLoadInput, sink clispec.ResultSink) error {
	duration, err := time.ParseDuration(input.Duration)
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_load_duration_refused", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen search-load: parse duration %q: %w", input.Duration, err)
	}
	signalContext, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	callContext := context.WithoutCancel(signalContext)
	summary, err := datagen.RunSearchLoad(signalContext, callContext, factory.Cfg, datagen.SearchLoadOptions{
		Seed: int64(input.Seed), Rate: input.Rate, Duration: duration,
		Concurrency: input.Concurrency, Commit: input.Commit,
	})
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_load_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen search-load: run workload: %w", err)
	}
	completedPerMinute := 0.0
	if summary.Elapsed > 0 {
		completedPerMinute = float64(summary.Completed) / summary.Elapsed.Minutes()
	}
	result := datagenSearchLoadResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.qa.datagen.search_load",
		DryRun: summary.DryRun, Seed: int64(input.Seed), TargetRatePerMinute: input.Rate,
		Duration: duration.String(), Concurrency: input.Concurrency, Planned: summary.Planned,
		Sent: summary.Sent, Completed: summary.Completed, Dropped: summary.Dropped,
		DistinctQueryTexts: summary.DistinctQueries, CompletedPerMinute: completedPerMinute,
		Elapsed: summary.Elapsed.String(),
		Errors: datagenSearchLoadErrors{
			Unavailable: summary.Unavailable, ToolError: summary.ToolErrors, TransportError: summary.TransportErrors,
		},
		LatencyMs: datagenSearchLoadLatency{
			Min: searchLoadMilliseconds(summary.Latency.Min), P50: searchLoadMilliseconds(summary.Latency.P50),
			P95: searchLoadMilliseconds(summary.Latency.P95), P99: searchLoadMilliseconds(summary.Latency.P99),
			Max: searchLoadMilliseconds(summary.Latency.Max),
		},
	}
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_load_result_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen search-load: write result: %w", err)
	}
	return nil
}

func searchLoadMilliseconds(latency time.Duration) float64 {
	return float64(latency) / float64(time.Millisecond)
}
