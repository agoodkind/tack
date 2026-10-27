package testenv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/clock"
)

const (
	// OpenSearchStopTimeout bounds how long StopOpenSearch waits for the engine
	// to exit after SIGTERM before Docker sends SIGKILL. It equals the
	// stop_grace_period of the opensearch service in docker-compose.yml.
	OpenSearchStopTimeout = 2 * time.Minute
	// sigkillExitCode is the exit status of a process that SIGKILL ended.
	sigkillExitCode = 137
)

// OpenSearchStop is the exit code of one engine stop and the time the stop
// request took.
type OpenSearchStop struct {
	ExitCode int
	Duration time.Duration
}

// StopOpenSearch sends SIGTERM to the engine container of fixture and waits
// up to OpenSearchStopTimeout for it to exit. It returns the exit code and the
// stop duration. It returns an error with the container state and the engine
// log tail when the engine exited with SIGKILL or the kernel killed it for
// memory.
func StopOpenSearch(ctx context.Context, fixture OpenSearchFixture) (OpenSearchStop, error) {
	stop := OpenSearchStop{ExitCode: 0, Duration: 0}
	cli, err := dockerClient(ctx)
	if err != nil {
		return stop, err
	}
	defer func() { _ = cli.Close() }()
	timeoutSeconds := int(OpenSearchStopTimeout / time.Second)
	started := clock.Now()
	if _, err := cli.ContainerStop(ctx, fixture.Container, client.ContainerStopOptions{Signal: "", Timeout: &timeoutSeconds}); err != nil {
		slog.ErrorContext(ctx, "testenv.engine.stop_failed", slog.String("err", err.Error()), slog.String("container", fixture.Container))
		return stop, fmt.Errorf("stop engine %s: %w", fixture.Container, err)
	}
	stop.Duration = clock.Now().Sub(started)
	state, err := engineState(ctx, cli, fixture.Container)
	if err != nil {
		return stop, err
	}
	stop.ExitCode = state.ExitCode
	slog.InfoContext(ctx, "testenv.engine.stopped", slog.String("container", fixture.Container),
		slog.Int("exit_code", state.ExitCode), slog.Duration("duration", stop.Duration))
	if state.ExitCode == sigkillExitCode || state.OOMKilled {
		return stop, fmt.Errorf("engine %s did not exit on SIGTERM within %s: %s",
			fixture.Container, OpenSearchStopTimeout, describeEngine(ctx, cli, fixture.Container, state))
	}
	return stop, nil
}

// OpenSearchDiagnostics returns the container state and the last engine log
// lines of the engine container of fixture, for a test failure message.
func OpenSearchDiagnostics(ctx context.Context, fixture OpenSearchFixture) string {
	cli, err := dockerClient(ctx)
	if err != nil {
		return "the engine state is unavailable: " + err.Error()
	}
	defer func() { _ = cli.Close() }()
	state, err := engineState(ctx, cli, fixture.Container)
	if err != nil {
		return "the engine state is unavailable: " + err.Error()
	}
	return describeEngine(ctx, cli, fixture.Container, state)
}

// engineState reads the state of container name.
func engineState(ctx context.Context, cli *client.Client, name string) (*container.State, error) {
	inspected, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{Size: false})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.engine.inspect_failed", slog.String("err", err.Error()), slog.String("container", name))
		return nil, fmt.Errorf("inspect engine %s: %w", name, err)
	}
	if inspected.Container.State == nil {
		return nil, errors.New("inspect engine " + name + ": Docker returned no container state")
	}
	return inspected.Container.State, nil
}

// describeEngine returns the status, exit code, out-of-memory flag, error,
// and finish time of state, followed by the last engineLogLines lines of
// container name.
func describeEngine(ctx context.Context, cli *client.Client, name string, state *container.State) string {
	logs, err := engineLogTail(ctx, cli, name)
	if err != nil {
		logs = "the engine log is unavailable: " + err.Error()
	}
	return fmt.Sprintf("engine %s status %s, exit code %d, OOM killed %t, error %q, finished at %s\nlast %s engine log lines:\n%s",
		name, state.Status, state.ExitCode, state.OOMKilled, state.Error, state.FinishedAt, engineLogLines, logs)
}
