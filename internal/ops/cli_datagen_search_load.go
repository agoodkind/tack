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
	Endpoints   string
	MaxWallTime string
}

func datagenSearchLoadOp(f *cli.Factory) clispec.Operation[datagenSearchLoadInput] {
	return clispec.Operation[datagenSearchLoadInput]{
		Name:     clispec.Name{Canonical: "search_load", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsDatagenSoak), Mutates: true},
		Group:    datagenGroup,
		Short:    "Send first-page tack_search requests at a target rate.",
		Long:     "ops qa datagen search-load sends first-page public tack_search calls with distinct query texts at the target rate in requests per minute set by --rate. The command counts a scheduled request as dropped without sending it when requests in flight equal --concurrency. Without --commit, the command performs a dry run. With --endpoints, the command accepts comma-separated base URLs and sends every tack_search call over HTTP to /mcp on those deployed Tack processes. Successive calls use endpoints in list order and repeat the list. Without --endpoints, the command builds a runtime graph and calls tack_search in process. With --commit, both modes write generated identities to FoundationDB and YugabyteDB. The command reads OpenSearch index statistics for the node-pages alias, which selects the serving index, and requests only the search metric group. It records point_in_time_current from the total section, which counts open point-in-time contexts across all shard copies of the index. Tack opens one point in time per search session. The command reads this count before the first request, every five seconds during the run, and after the last request finishes. These reads use the runtime OpenSearch credential in OPENSEARCH_USERNAME and OPENSEARCH_PASSWORD, which the app service provides. If a request fails, OpenSearch returns an HTTP error status, or the alias does not select exactly one index, the command records an unavailable sample with the HTTP status and error text and continues the run. The sample records status as zero when the request did not receive a response. The command logs the first unavailable sample of the run once at Error level. The result reports the endpoint count as endpoints and the point-in-time report as point_in_time. That report includes peak, the largest count among successful reads, answered_samples, the number of successful reads, and samples with offset_ms, index, open, unavailable, status, and error. The result also reports sent, completed, and dropped requests, completed requests per minute, errors by class as unavailable, tool_error, and transport_error, and latency percentiles. The --max-wall-time bound starts when the command starts and also covers setup. When the bound expires, SIGINT arrives, or SIGTERM arrives, the command stops scheduling requests, waits for requests in flight, and writes the result. The result reports the bound as max_wall_time and the reason as stop_reason: max_wall_time when the bound ended the schedule, signal when SIGINT or SIGTERM ended it, and none when the schedule finished.",
		Params: []clispec.Param[datagenSearchLoadInput]{
			clispec.IntParam("rate", "Set the target rate in requests per minute.", 0, func(input *datagenSearchLoadInput, value int) { input.Rate = value }),
			clispec.StringParam("duration", "Set the request scheduling duration.", defaultDatagenSearchLoadDuration, false, func(input *datagenSearchLoadInput, value string) { input.Duration = value }),
			clispec.IntParam("concurrency", "Set the maximum number of requests in flight.", defaultDatagenSearchLoadConcurrency, func(input *datagenSearchLoadInput, value int) { input.Concurrency = value }),
			clispec.IntParam("seed", "Set the random seed.", defaultDatagenSeed, func(input *datagenSearchLoadInput, value int) { input.Seed = value }),
			clispec.BoolParam("commit", "Execute the search workload.", false, func(input *datagenSearchLoadInput, value bool) { input.Commit = value }),
			clispec.StringParam("endpoints", "Send every tack_search call over HTTP to /mcp on the deployed Tack processes at these comma-separated base URLs. Without --endpoints, the command builds and calls an in-process runtime graph.", "", false, func(input *datagenSearchLoadInput, value string) { input.Endpoints = value }),
			clispec.StringParam("max-wall-time", "Set the longest time the command schedules requests, counted from command start.", defaultDatagenSearchMaxWallTime, false, func(input *datagenSearchLoadInput, value string) { input.MaxWallTime = value }),
		},
		New: func() datagenSearchLoadInput {
			return datagenSearchLoadInput{
				InputMarker: clispec.InputMarker{}, Rate: 0, Duration: defaultDatagenSearchLoadDuration,
				Concurrency: defaultDatagenSearchLoadConcurrency, Seed: defaultDatagenSeed, Commit: false, Endpoints: "",
				MaxWallTime: defaultDatagenSearchMaxWallTime,
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
	endpoints, err := parseSearchMixedEndpoints(input.Endpoints)
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_load_endpoints_refused", slog.String("err", err.Error()))
		return err
	}
	maxWallTime, err := parseDatagenSearchMaxWallTime(ctx, "search-load", input.MaxWallTime)
	if err != nil {
		return err
	}
	stopContext, stopSchedule := datagenSearchStopContext(ctx, maxWallTime)
	defer stopSchedule()
	callContext := context.WithoutCancel(stopContext)
	summary, err := datagen.RunSearchLoad(stopContext, callContext, factory.Cfg, datagen.SearchLoadOptions{
		Seed: int64(input.Seed), Rate: input.Rate, Duration: duration,
		Concurrency: input.Concurrency, Commit: input.Commit,
		Endpoints: endpoints, Client: &http.Client{Timeout: datagenSearchTwoProcessTimeout},
	})
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_load_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen search-load: run workload: %w", err)
	}
	stop := datagenSearchStopped(stopContext, maxWallTime, summary.Stopped)
	logDatagenSearchStop(ctx, "search-load", stop)
	if err := clispec.WriteJSONValue(ctx, sink, searchLoadReport(input, duration, len(endpoints), summary, stop)); err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_load_result_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen search-load: write result: %w", err)
	}
	return nil
}

func searchLoadMilliseconds(latency time.Duration) float64 {
	return float64(latency) / float64(time.Millisecond)
}
