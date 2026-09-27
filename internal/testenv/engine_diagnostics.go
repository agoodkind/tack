package testenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	// engineLogLines is the number of engine log lines a start failure
	// includes.
	engineLogLines = "80"
	// engineLogTimeout bounds the log read after a start failure. The read
	// uses its own deadline because the provisioning deadline may have passed.
	engineLogTimeout = 30 * time.Second
)

// errEngineExited reports that an engine container stopped before it became
// ready.
var errEngineExited = errors.New("engine container exited before it became ready")

// cancelOnExit waits until container name stops running, then cancels ctx
// with errEngineExited and the exit status. It also returns when ctx ends or
// when the wait request fails, and it cancels nothing in those cases.
func cancelOnExit(ctx context.Context, cli *client.Client, name string, cancel context.CancelCauseFunc) {
	waited := cli.ContainerWait(ctx, name, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case <-ctx.Done():
	case response := <-waited.Result:
		cancel(fmt.Errorf("%w with status %d", errEngineExited, response.StatusCode))
	case err := <-waited.Error:
		slog.DebugContext(ctx, "testenv.engine.wait_unavailable", slog.String("container", name), slog.String("reason", err.Error()))
	}
}

// engineStartFailure returns cause with the last engineLogLines lines of
// container name appended.
func engineStartFailure(ctx context.Context, cli *client.Client, name string, cause error) error {
	logs, err := engineLogTail(ctx, cli, name)
	if err != nil {
		logs = "the engine log is unavailable: " + err.Error()
	}
	wrapped := fmt.Errorf("start engine %s: %w\nlast %s engine log lines:\n%s", name, cause, engineLogLines, logs)
	slog.ErrorContext(ctx, "testenv.engine.start_failed", slog.String("err", wrapped.Error()), slog.String("container", name))
	return wrapped
}

// engineLogTail reads the last engineLogLines lines of stdout and stderr of
// container name.
func engineLogTail(ctx context.Context, cli *client.Client, name string) (string, error) {
	readContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), engineLogTimeout)
	defer cancel()
	stream, err := cli.ContainerLogs(readContext, name, client.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Since: "", Until: "", Timestamps: false,
		Follow: false, Tail: engineLogLines, Details: false,
	})
	if err != nil {
		slog.DebugContext(ctx, "testenv.engine.logs_unavailable", slog.String("container", name), slog.String("reason", err.Error()))
		return "", errors.New("read the log of " + name + ": " + err.Error())
	}
	defer func() { _ = stream.Close() }()
	var output bytes.Buffer
	if _, err := stdcopy.StdCopy(&output, &output, stream); err != nil {
		slog.DebugContext(ctx, "testenv.engine.logs_unreadable", slog.String("container", name), slog.String("reason", err.Error()))
		return output.String(), errors.New("demultiplex the log of " + name + ": " + err.Error())
	}
	return output.String(), nil
}
