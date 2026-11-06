package testenv

import (
	"context"
	"log/slog"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// logFailedEngine records an abnormal terminal state before owner cleanup
// removes the fixture. Observation failures do not prevent removal.
func logFailedEngine(ctx context.Context, cli *client.Client, name string) {
	observation, cancel := context.WithTimeout(ctx, engineLogTimeout)
	defer cancel()
	state, err := engineState(observation, cli, name)
	if cerrdefs.IsNotFound(err) {
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "testenv.engine.cleanup_observation_failed", slog.String("container", name), slog.String("err", err.Error()))
		return
	}
	if state.Status != container.StateExited && state.Status != container.StateDead {
		return
	}
	if state.ExitCode == 0 && !state.OOMKilled && state.Error == "" {
		return
	}
	slog.ErrorContext(ctx, "testenv.engine.exited", slog.String("container", name),
		slog.String("err", describeEngine(observation, cli, name, state)))
}
