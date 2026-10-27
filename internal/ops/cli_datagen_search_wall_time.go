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
	// datagenSearchStopCanceled reports that the parent context of the command ended.
	datagenSearchStopCanceled datagenSearchStopReason = "canceled"
)

// errDatagenSearchMaxWallTime is the cancellation cause of the schedule context when
// --max-wall-time expires.
var errDatagenSearchMaxWallTime = errors.New("qa datagen: the --max-wall-time bound expired")

// datagenSearchStop records the wall-time bound, the reason that the schedule ended, and the
// cancellation cause text of the schedule context.
type datagenSearchStop struct {
	maxWallTime time.Duration
	reason      datagenSearchStopReason
	cause       string
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

// datagenSearchStopContext returns the signal context and the schedule context of a search
// workload. SIGINT and SIGTERM end the signal context. The schedule context ends when the signal
// context ends or maxWallTime expires.
func datagenSearchStopContext(ctx context.Context, maxWallTime time.Duration) (context.Context, context.Context, context.CancelFunc) {
	signalContext, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	boundedContext, stopBound := context.WithTimeoutCause(signalContext, maxWallTime, errDatagenSearchMaxWallTime)
	return signalContext, boundedContext, func() {
		stopBound()
		stopSignals()
	}
}

// datagenSearchStopped returns the stop record for a run. stopped reports whether the workload
// ended its schedule early because the schedule context ended. The reason is max_wall_time when
// the cause of the schedule context is the bound, and signal when the signal context ended with a
// cause that differs from the cause of parent. Every other cause is canceled.
func datagenSearchStopped(parent, signalContext, stopContext context.Context, maxWallTime time.Duration, stopped bool) datagenSearchStop {
	if !stopped {
		return datagenSearchStop{maxWallTime: maxWallTime, reason: datagenSearchStopNone, cause: ""}
	}
	cause := context.Cause(stopContext)
	causeText := ""
	if cause != nil {
		causeText = cause.Error()
	}
	signalCause := context.Cause(signalContext)
	reason := datagenSearchStopCanceled
	if errors.Is(cause, errDatagenSearchMaxWallTime) {
		reason = datagenSearchStopMaxWallTime
	} else if signalCause != nil && !errors.Is(signalCause, context.Cause(parent)) {
		reason = datagenSearchStopSignal
	}
	return datagenSearchStop{maxWallTime: maxWallTime, reason: reason, cause: causeText}
}

// logDatagenSearchStop logs qa.datagen.search_stopped when a signal, the wall-time bound, or the
// end of the parent context ended the run.
func logDatagenSearchStop(ctx context.Context, command string, stop datagenSearchStop) {
	if stop.reason == datagenSearchStopNone {
		return
	}
	slog.InfoContext(ctx, "qa.datagen.search_stopped", slog.String("command", command),
		slog.String("reason", string(stop.reason)), slog.String("cause", stop.cause),
		slog.Duration("max_wall_time", stop.maxWallTime))
}
