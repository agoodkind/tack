package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

func (s *Soak) stopReason(
	ctx context.Context,
	stop <-chan struct{},
	startedAt time.Time,
) string {
	select {
	case <-stop:
		return "signal"
	default:
	}
	select {
	case <-ctx.Done():
		return "context"
	default:
	}
	if s.options.MaxOps > 0 && s.summary.Operations >= s.options.MaxOps {
		return "max_ops"
	}
	if s.options.Duration > 0 && s.clock.Since(startedAt) >= s.options.Duration {
		return "duration"
	}
	return ""
}

func (s *Soak) stopResult(
	ctx context.Context,
	startedAt time.Time,
	reason string,
) (SoakSummary, error) {
	summary := s.finish(ctx, startedAt, reason)
	if reason != "context" {
		return summary, nil
	}
	slog.ErrorContext(ctx, "qa.datagen.soak_context_done",
		slog.String("err", ctx.Err().Error()),
	)
	return summary, fmt.Errorf("qa datagen soak: context canceled: %w", ctx.Err())
}

func (s *Soak) finish(
	ctx context.Context,
	startedAt time.Time,
	reason string,
) SoakSummary {
	s.summary.StopReason = reason
	s.summary.Elapsed = s.clock.Since(startedAt)
	slog.InfoContext(
		ctx,
		"qa.datagen.soak_stopped",
		slog.String("reason", reason),
		slog.Int("operations", s.summary.Operations),
	)
	return s.summary
}

func validateSoakOptions(options SoakOptions) error {
	if options.Duration < 0 {
		return fmt.Errorf("qa datagen soak: duration must be zero or positive")
	}
	if options.Rate <= 0 {
		return fmt.Errorf("qa datagen soak: rate must be positive")
	}
	if options.MaxOps < 0 {
		return fmt.Errorf("qa datagen soak: max-ops must be zero or positive")
	}
	return nil
}
