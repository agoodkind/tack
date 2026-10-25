package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// defaultDatagenSearchMaxWallTime bounds search-load and search-mixed when the operator does not
// set --max-wall-time.
const defaultDatagenSearchMaxWallTime = "20m"

// datagenSearchStopReason reports what ended the schedule of a search workload before it
// finished.
type datagenSearchStopReason string

const (
	datagenSearchStopNone        datagenSearchStopReason = "none"
	datagenSearchStopSignal      datagenSearchStopReason = "signal"
	datagenSearchStopMaxWallTime datagenSearchStopReason = "max_wall_time"
)

// errDatagenSearchMaxWallTime is the cancellation cause of the schedule context when
// --max-wall-time expires.
var errDatagenSearchMaxWallTime = errors.New("qa datagen: the --max-wall-time bound expired")

// datagenSearchStop records the wall-time bound and the reason that the schedule ended.
type datagenSearchStop struct {
	maxWallTime time.Duration
	reason      datagenSearchStopReason
}

// parseDatagenSearchMaxWallTime parses --max-wall-time and refuses a bound that is not positive.
func parseDatagenSearchMaxWallTime(ctx context.Context, command, value string) (time.Duration, error) {
	maxWallTime, err := time.ParseDuration(value)
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_max_wall_time_refused", slog.String("command", command), slog.String("err", err.Error()))
		return 0, fmt.Errorf("qa datagen %s: the command could not parse max-wall-time %q: %w", command, value, err)
	}
	if maxWallTime <= 0 {
		return 0, fmt.Errorf("qa datagen %s: max-wall-time %s must be positive", command, maxWallTime)
	}
	return maxWallTime, nil
}

// datagenSearchStopContext returns the schedule context of a search workload. SIGINT, SIGTERM,
// and the expiry of maxWallTime each end it.
func datagenSearchStopContext(ctx context.Context, maxWallTime time.Duration) (context.Context, context.CancelFunc) {
	signalContext, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	boundedContext, stopBound := context.WithTimeoutCause(signalContext, maxWallTime, errDatagenSearchMaxWallTime)
	return boundedContext, func() {
		stopBound()
		stopSignals()
	}
}

// datagenSearchStopped returns the stop record for a run. stopped reports whether the workload
// ended its schedule early because the schedule context ended.
func datagenSearchStopped(ctx, stopContext context.Context, command string, maxWallTime time.Duration, stopped bool) datagenSearchStop {
	reason := datagenSearchStopNone
	if stopped {
		reason = datagenSearchStopSignal
		if errors.Is(context.Cause(stopContext), errDatagenSearchMaxWallTime) {
			reason = datagenSearchStopMaxWallTime
		}
		slog.InfoContext(ctx, "qa.datagen.search_stopped", slog.String("command", command),
			slog.String("reason", string(reason)), slog.Duration("max_wall_time", maxWallTime))
	}
	return datagenSearchStop{maxWallTime: maxWallTime, reason: reason}
}
