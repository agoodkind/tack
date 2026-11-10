package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const defaultBurstWindow = 5 * time.Second

type burstPhase string

const (
	burstQuiet burstPhase = "quiet"
	burstSpike burstPhase = "spike"
)

func phaseAt(elapsed, window time.Duration) burstPhase {
	if window <= 0 {
		window = defaultBurstWindow
	}
	if elapsed/window%2 == 0 {
		return burstQuiet
	}
	return burstSpike
}

func phaseRate(targetRate int, phase burstPhase) float64 {
	if phase == burstSpike {
		return float64(targetRate) * 1.6
	}
	return float64(targetRate) * 0.4
}

func phaseInterval(targetRate int, phase burstPhase) time.Duration {
	rate := phaseRate(targetRate, phase)
	return time.Duration(float64(time.Second) / rate)
}

func waitForSoakOperation(
	ctx context.Context,
	stop <-chan struct{},
	delay time.Duration,
) (bool, error) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-stop:
		return false, nil
	default:
	}
	select {
	case <-timer.C:
		return true, nil
	case <-stop:
		return false, nil
	case <-ctx.Done():
		select {
		case <-stop:
			return false, nil
		default:
		}
		slog.ErrorContext(ctx, "qa.datagen.soak_wait_failed",
			slog.String("err", ctx.Err().Error()),
		)
		return false, fmt.Errorf("qa datagen soak: wait for operation: %w", ctx.Err())
	}
}
