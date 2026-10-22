package ops

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
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
	defaultDatagenSearchMixedBurstWrites = 200
	defaultDatagenSearchMixedCatchUp     = "10m"
)

type datagenSearchMixedInput struct {
	clispec.InputMarker
	Rate        int
	WriteRate   int
	Duration    string
	Concurrency int
	Seed        int
	Commit      bool
	Endpoints   string
	BurstWrites int
	CatchUp     string
	Rebuild     bool
}

func datagenSearchMixedOp(f *cli.Factory) clispec.Operation[datagenSearchMixedInput] {
	return clispec.Operation[datagenSearchMixedInput]{
		Name:     clispec.Name{Canonical: "search_mixed", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsDatagenSoak), Mutates: true},
		Group:    datagenGroup,
		Short:    "Measure search during concurrent writes, an ingestion burst, permission changes, and an optional index rebuild.",
		Long:     "The command sends first-page public tack_search calls at the offered rate across two tenants. It schedules public calls to create, edit, and delete issues at the write rate. Each created or edited issue contains a unique marker word. Freshness lag measures the time from the issue update time that FoundationDB stores to the first search that returns the issue for its marker. At one quarter of the duration, the command creates the burst issues back to back. It then deletes issues and checks session continuation, page replay, and a new search while deletion work can still be pending. Any result that includes a deleted issue fails the check. The command also removes a member and repeats these checks. It allows cached membership access for AUTH_MEMBERSHIP_CACHE_LIFETIME after removal. Any search that returns an issue after that lifetime fails the check. The command also fails if any call still permits access after that lifetime plus the convergence deadline. Read sessions search 140 unchanged issues in the first tenant and 10 in the second. Their pages alternate between endpoints. The command repeats each continued page request using the next endpoint. With --rebuild, the command begins a full index rebuild using the same operation as search reindex --mode full. After the writes and phases end, the command waits up to --catch-up for live, access, and cleanup search work with enqueue times before that moment to finish. Every two seconds, the result records the age of the oldest pending search work and each OpenSearch node's CPU, memory, JVM heap, and disk. The command does not use rates, latency, or throughput as pass thresholds. Without --commit, the command performs a dry run. The command writes the result before returning an error for a workload phase failure or a failed correctness check, but it returns a setup error without writing a result.",
		Params: []clispec.Param[datagenSearchMixedInput]{
			clispec.IntParam("rate", "Set the offered number of first-page public tack_search calls per minute.", 0, func(input *datagenSearchMixedInput, value int) { input.Rate = value }),
			clispec.IntParam("write-rate", "Set the number of scheduled issue writes per minute.", 0, func(input *datagenSearchMixedInput, value int) { input.WriteRate = value }),
			clispec.StringParam("duration", "Set the duration of the search and write schedules.", defaultDatagenSearchLoadDuration, false, func(input *datagenSearchMixedInput, value string) { input.Duration = value }),
			clispec.IntParam("concurrency", "Set the maximum number of concurrent load searches.", defaultDatagenSearchLoadConcurrency, func(input *datagenSearchMixedInput, value int) { input.Concurrency = value }),
			clispec.IntParam("seed", "Set the seed for generated identities and search queries.", defaultDatagenSeed, func(input *datagenSearchMixedInput, value int) { input.Seed = value }),
			clispec.BoolParam("commit", "Execute public searches and issue writes.", false, func(input *datagenSearchMixedInput, value bool) { input.Commit = value }),
			clispec.StringParam("endpoints", "Send every public call over HTTP to the deployed Tack processes at these comma-separated base URLs. Without this flag, the command uses an in-process runtime.", "", false, func(input *datagenSearchMixedInput, value string) { input.Endpoints = value }),
			clispec.IntParam("burst-writes", "Set the number of issues that the ingestion burst creates.", defaultDatagenSearchMixedBurstWrites, func(input *datagenSearchMixedInput, value int) { input.BurstWrites = value }),
			clispec.StringParam("catch-up", "Set the longest wait for search work after the writes end.", defaultDatagenSearchMixedCatchUp, false, func(input *datagenSearchMixedInput, value string) { input.CatchUp = value }),
			clispec.BoolParam("rebuild", "Begin one full index rebuild after the ingestion burst.", false, func(input *datagenSearchMixedInput, value bool) { input.Rebuild = value }),
		},
		New: func() datagenSearchMixedInput {
			return datagenSearchMixedInput{
				InputMarker: clispec.InputMarker{}, Rate: 0, WriteRate: 0, Duration: defaultDatagenSearchLoadDuration,
				Concurrency: defaultDatagenSearchLoadConcurrency, Seed: defaultDatagenSeed, Commit: false, Endpoints: "",
				BurstWrites: defaultDatagenSearchMixedBurstWrites, CatchUp: defaultDatagenSearchMixedCatchUp, Rebuild: false,
			}
		},
		Run: func(ctx context.Context, input datagenSearchMixedInput, sink clispec.ResultSink) error {
			return runDatagenSearchMixed(ctx, f, input, sink)
		},
	}
}

func runDatagenSearchMixed(ctx context.Context, factory *cli.Factory, input datagenSearchMixedInput, sink clispec.ResultSink) error {
	options, err := searchMixedOptions(ctx, factory, input)
	if err != nil {
		return err
	}
	signalContext, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	callContext := context.WithoutCancel(signalContext)
	summary, runErr := datagen.RunSearchMixed(signalContext, callContext, factory.Cfg, options)
	if runErr != nil && !summary.Measured {
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_failed", slog.String("err", runErr.Error()))
		return fmt.Errorf("qa datagen search-mixed: the workload failed: %w", runErr)
	}
	if err := clispec.WriteJSONValue(ctx, sink, searchMixedResult(input, options.Duration, summary)); err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_result_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen search-mixed: the command could not write the result: %w", err)
	}
	if runErr != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_phase_failed", slog.String("err", runErr.Error()))
		return fmt.Errorf("qa datagen search-mixed: a workload phase failed: %w", runErr)
	}
	if err := summary.Verify(ctx); err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_checks_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen search-mixed: a correctness check failed: %w", err)
	}
	return nil
}

func searchMixedOptions(ctx context.Context, factory *cli.Factory, input datagenSearchMixedInput) (datagen.SearchMixedOptions, error) {
	var options datagen.SearchMixedOptions
	duration, err := time.ParseDuration(input.Duration)
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_duration_refused", slog.String("err", err.Error()))
		return options, fmt.Errorf("qa datagen search-mixed: the command could not parse duration %q: %w", input.Duration, err)
	}
	catchUp, err := time.ParseDuration(input.CatchUp)
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_catch_up_refused", slog.String("err", err.Error()))
		return options, fmt.Errorf("qa datagen search-mixed: the command could not parse catch-up %q: %w", input.CatchUp, err)
	}
	endpoints, err := parseSearchMixedEndpoints(input.Endpoints)
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_endpoints_refused", slog.String("err", err.Error()))
		return options, err
	}
	options = datagen.SearchMixedOptions{
		Seed: int64(input.Seed), Rate: input.Rate, WriteRate: input.WriteRate, Duration: duration,
		Concurrency: input.Concurrency, Commit: input.Commit, BurstWrites: input.BurstWrites, CatchUp: catchUp,
		Endpoints: endpoints, Client: &http.Client{Timeout: datagenSearchTwoProcessTimeout}, Rebuild: nil,
	}
	if input.Rebuild {
		options.Rebuild = searchMixedRebuild(factory)
	}
	return options, nil
}
