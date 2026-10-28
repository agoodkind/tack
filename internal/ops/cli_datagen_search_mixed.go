package ops

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
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
	MaxWallTime string
}

func datagenSearchMixedOp(f *cli.Factory) clispec.Operation[datagenSearchMixedInput] {
	return clispec.Operation[datagenSearchMixedInput]{
		Name:     clispec.Name{Canonical: "search_mixed", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsDatagenSoak), Mutates: true},
		Group:    datagenGroup,
		Short:    "Measure search during concurrent writes, an ingestion burst, permission changes, and an optional index rebuild.",
		Long:     "The command sends first-page public tack_search calls at the offered rate across two tenants. It schedules public calls to create, edit, and delete issues at the write rate. Each created or edited issue contains a unique marker word. Freshness lag measures the time from the issue update time that FoundationDB stores to the first search that returns the issue for its marker. At one quarter of the duration, the command creates the burst issues back to back. It then deletes issues and checks session continuation, page replay, and a new search while deletion work can still be pending. Any result that includes a deleted issue fails the check. The command also removes a member and repeats these checks. It allows cached membership access for AUTH_MEMBERSHIP_CACHE_LIFETIME after removal. Any search that returns an issue after that lifetime fails the check. The command also fails if any call still permits access after that lifetime plus the convergence deadline. Read sessions search 140 unchanged issues in the first tenant and 10 in the second. Their pages alternate between endpoints. The command repeats each continued page request using the next endpoint. With --rebuild, the command starts a full index rebuild with the same audited operation as ops search reindex --mode full --execute and records the rebuild separately under ops.search.reindex. After the writes and phases end, the command waits up to --catch-up for live, access, and cleanup search work with enqueue times before that moment to finish. A measured run that finishes fails the correctness check if pending search work does not finish within --catch-up, if search does not return a written issue for its marker word, or if the command cannot verify that search stopped returning a deleted issue. A measured run also fails the correctness check if --max-wall-time, SIGINT, SIGTERM, or the end of the parent context stops it; the error states the stop reason and states that the command did not run the catch-up wait or the marker and deletion verification. Every two seconds, the result records the age of the oldest pending search work and each OpenSearch node's CPU, memory, JVM heap, and disk. Each sample also lists every class with pending search work, the number of buckets in that class that contain at least one age key, and whether the class's oldest age key has a live work record. The command does not use rates, latency, or throughput as pass thresholds. Without --commit, the command performs a dry run. After the measured window ends, the command writes the result before returning an error for a workload phase failure, a recorded goroutine panic, or a failed correctness check; it returns setup errors and other errors before that point without writing a result. Before it writes the result, the command logs every result field except the samples as one Info record named qa.datagen.search_mixed_result. The --max-wall-time bound starts when the command starts. When the bound expires, SIGINT arrives, or SIGTERM arrives, the command stops search loads and scheduled writes, skips the catch-up wait, ends the probe wait, and writes the result. The command finishes the workload phase in progress and skips every phase that has not started. A revocation check in progress always restores the removed membership. Setup waits until search returns every corpus issue. A setup wait fails when the number of corpus issues that search omits does not decrease for two minutes, and the error reports the omitted count and the first omitted issue. The result lists each finished setup wait in corpus_index.waits with its group, tenant, issue count, check count, and elapsed_ms, the time from the last setup create to the end of the first check that returned every issue of the group. corpus_index.threshold states that no documented product target defines a threshold for this time. If the bound or a signal ends a setup wait for search to return the corpus, the command writes a result with only the finished setup waits and skips every phase. The result lists each skipped phase in skipped_phases as sessions, burst, pending_deletion, revocation, rebuild, or rebuild_sessions. The result reports the bound as max_wall_time and the reason as stop_reason: max_wall_time when the bound ended the run, signal when SIGINT or SIGTERM ended it, canceled when the parent context of the command ended, and none when the run finished.",
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
			clispec.StringParam("max-wall-time", "Set the longest time before the command stops loads and writes, counted from command start.", defaultDatagenSearchMaxWallTime, false, func(input *datagenSearchMixedInput, value string) { input.MaxWallTime = value }),
		},
		New: func() datagenSearchMixedInput {
			return datagenSearchMixedInput{
				InputMarker: clispec.InputMarker{}, Rate: 0, WriteRate: 0, Duration: defaultDatagenSearchLoadDuration,
				Concurrency: defaultDatagenSearchLoadConcurrency, Seed: defaultDatagenSeed, Commit: false, Endpoints: "",
				BurstWrites: defaultDatagenSearchMixedBurstWrites, CatchUp: defaultDatagenSearchMixedCatchUp, Rebuild: false,
				MaxWallTime: defaultDatagenSearchMaxWallTime,
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
	maxWallTime, err := parseDatagenSearchMaxWallTime(ctx, "search-mixed", input.MaxWallTime)
	if err != nil {
		return err
	}
	signalContext, stopContext, stopSchedule := datagenSearchStopContext(ctx, maxWallTime)
	defer stopSchedule()
	callContext := context.WithoutCancel(stopContext)
	summary, runErr := datagen.RunSearchMixed(stopContext, callContext, factory.Cfg, options)
	if runErr != nil && !summary.Measured {
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_failed", slog.String("err", runErr.Error()))
		return fmt.Errorf("qa datagen search-mixed: the workload failed: %w", runErr)
	}
	stop := datagenSearchStopped(ctx, signalContext, stopContext, maxWallTime, summary.Stopped)
	logDatagenSearchStop(ctx, "search-mixed", stop)
	result := searchMixedResult(input, options.Duration, summary, stop)
	logSearchMixedResult(ctx, result)
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_result_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen search-mixed: the command could not write the result: %w", err)
	}
	if runErr != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_phase_failed", slog.String("err", runErr.Error()))
		return fmt.Errorf("qa datagen search-mixed: a workload phase failed: %w", runErr)
	}
	if err := summary.Verify(ctx, string(stop.reason)); err != nil {
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
