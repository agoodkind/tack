package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

// waitExecOK polls a scratch engine until its check succeeds or timeout ends.
func waitExecOK(ctx context.Context, r *restoreDrillCtx, container string, timeout time.Duration, env []string, cmd []string) error {
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		exitCode, _, err := containerExecStreaming(pollCtx, r.Cli, container, cmd, env, devNull{})
		if err == nil && exitCode == 0 {
			return nil
		}
		select {
		case <-pollCtx.Done():
			err := fmt.Errorf("readiness check %v timed out after %s: %w", cmd, timeout, pollCtx.Err())
			telemetry.L(ctx).WarnContext(ctx, "backup.restore_drill.wait_timeout",
				slog.String("container", container), slog.String("err", err.Error()))
			return err
		case <-ticker.C:
		}
	}
}
