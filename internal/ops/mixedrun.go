package ops

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/datagen"
)

func runDatagenSearchMixed(
	ctx context.Context,
	factory *cli.Factory,
	input datagenSearchMixedInput,
	sink clispec.ResultSink,
) error {
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
		slog.ErrorContext(
			ctx,
			"qa.datagen.search_mixed_result_failed",
			slog.String("err", err.Error()),
		)

		return fmt.Errorf(
			"qa datagen search-mixed: the command could not write the result: %w",
			err,
		)
	}

	if runErr != nil {
		slog.ErrorContext(
			ctx,
			"qa.datagen.search_mixed_phase_failed",
			slog.String("err", runErr.Error()),
		)

		return fmt.Errorf("qa datagen search-mixed: a workload phase failed: %w", runErr)
	}

	if err := summary.Verify(ctx, string(stop.reason)); err != nil {
		slog.ErrorContext(
			ctx,
			"qa.datagen.search_mixed_checks_failed",
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("qa datagen search-mixed: a correctness check failed: %w", err)
	}

	return nil
}

func searchMixedOptions(ctx context.Context, factory *cli.Factory, input datagenSearchMixedInput) (
	datagen.SearchMixedOptions,
	error,
) {
	var options datagen.SearchMixedOptions
	duration, err := time.ParseDuration(input.Duration)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"qa.datagen.search_mixed_duration_refused",
			slog.String("err", err.Error()),
		)

		return options, fmt.Errorf(
			"qa datagen search-mixed: the command could not parse duration %q: %w",
			input.Duration,
			err,
		)
	}

	catchUp := time.Duration(0)
	if input.CatchUp != "" {
		catchUp, err = time.ParseDuration(input.CatchUp)
		if err != nil {
			slog.ErrorContext(
				ctx,
				"qa.datagen.search_mixed_catch_up_refused",
				slog.String("err", err.Error()),
			)

			return options, fmt.Errorf(
				"qa datagen search-mixed: the command could not parse catch-up %q: %w",
				input.CatchUp,
				err,
			)
		}
	}

	endpoints, err := parseSearchMixedEndpoints(input.Endpoints)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"qa.datagen.search_mixed_endpoints_refused",
			slog.String("err", err.Error()),
		)

		return options, err
	}

	options = datagen.SearchMixedOptions{
		Seed:              int64(input.Seed),
		Rate:              input.Rate,
		WriteRate:         input.WriteRate,
		Duration:          duration,
		Concurrency:       input.Concurrency,
		Commit:            input.Commit,
		BurstWrites:       input.BurstWrites,
		CatchUp:           catchUp,
		Endpoints:         endpoints,
		Client:            &http.Client{Timeout: datagenSearchTwoProcessTimeout},
		Rebuild:           nil,
		MetadataWrites:    input.Metadata,
		Moves:             input.Moves,
		ProbeAfterCatchUp: false,
	}
	if input.Rebuild {
		options.Rebuild = searchMixedRebuild(factory)
	}

	return options, nil
}
