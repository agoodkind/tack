package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// newDockerClient builds a docker client honoring DOCKER_HOST,
// DOCKER_CONTEXT, DOCKER_TLS_VERIFY, etc. The plan calls for laptop-side
// invocation with `DOCKER_CONTEXT=tack` over `ssh://`. The client.FromEnv
// option is exactly that.
func newDockerClient() (*client.Client, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		slog.Error("backup.docker.client_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return cli, nil
}

// assertDockerContext verifies that the named docker context is registered
// before the backup family starts spinning up containers. If the operator
// forgot to set up the `tack` context, every later call would fail with a
// confusing TLS or dial error; we fail fast with a clear message instead.
//
// `docker context inspect` is invoked via os/exec because the docker SDK
// does not expose context registration; the CLI owns that file.
func assertDockerContext(ctx context.Context, contextName string) error {
	if contextName == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, "docker", "context", "inspect", contextName)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		slog.ErrorContext(ctx, "backup.docker.context_missing",
			slog.String("context", contextName),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("docker context %q not configured: %w (stderr: %s)",
			contextName, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// runDockerCmd runs `docker <args...>` and returns combined output. Used by
// the few operations the SDK does not cover cleanly. The plan's "no shell
// scripts" rule allows individual typed command invocations; what it forbids
// is writing new .sh files.
func runDockerCmd(ctx context.Context, log *slog.Logger, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	log.DebugContext(ctx, "backup.docker.cmd",
		slog.Any("args", args),
		slog.Int("output_bytes", len(out)),
	)
	if err != nil {
		log.ErrorContext(ctx, "backup.docker.cmd_failed",
			slog.Any("args", args),
			slog.String("err", err.Error()),
			slog.String("output", strings.TrimSpace(string(out))),
		)
		return string(out), fmt.Errorf("docker %s: %w (output: %s)",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// execResult bundles a single ContainerExec invocation's stdout, stderr, and
// exit code so callers do not have to plumb the multiplexed stream by hand.
type execResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// containerExec runs cmd inside an existing container and returns the
// captured streams plus exit code. Equivalent to `docker exec` but typed
// and stream-safe.
func containerExec(
	ctx context.Context,
	cli *client.Client,
	containerID string,
	cmd []string,
	envVars []string,
) (execResult, error) {
	created, err := cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		Cmd:          cmd,
		Env:          envVars,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		slog.ErrorContext(ctx, "backup.docker.exec_create_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)
		return execResult{}, fmt.Errorf("exec create: %w", err)
	}
	att, err := cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "backup.docker.exec_attach_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)
		return execResult{}, fmt.Errorf("exec attach: %w", err)
	}
	defer att.Close()
	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, att.Reader)
	if err != nil && !errors.Is(err, io.EOF) {
		slog.ErrorContext(ctx, "backup.docker.exec_stream_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)
		return execResult{}, fmt.Errorf("exec stream: %w", err)
	}
	insp, err := cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "backup.docker.exec_inspect_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)
		return execResult{}, fmt.Errorf("exec inspect: %w", err)
	}
	return execResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: insp.ExitCode,
	}, nil
}

// waitForExec polls a check command in container until it exits 0 or the
// timeout elapses. Uses [context.WithTimeout] plus a re-usable ticker so
// the poll loop honors cancellation without consulting [time.Now] directly.
// Mirrors `wait_for_exec` in scripts/backup-restore-test.sh.
func waitForExec(
	ctx context.Context,
	cli *client.Client,
	containerID string,
	timeout time.Duration,
	cmd []string,
) error {
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		res, err := containerExec(pollCtx, cli, containerID, cmd, nil)
		if err == nil && res.ExitCode == 0 {
			return nil
		}
		select {
		case <-pollCtx.Done():
			slog.ErrorContext(ctx, "backup.docker.wait_done",
				slog.String("container", containerID),
				slog.Duration("timeout", timeout),
				slog.String("err", pollCtx.Err().Error()),
			)
			return fmt.Errorf("waitForExec %v: %w", cmd, pollCtx.Err())
		case <-ticker.C:
		}
	}
}

// removeContainerForce removes a container by name, ignoring not-found
// errors. Used by deferred teardown paths.
func removeContainerForce(ctx context.Context, cli *client.Client, name string) {
	_, _ = cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true})
}

// netMode constructs a NetworkingConfig that joins the named docker network.
func netMode(networkName string) *network.NetworkingConfig {
	if networkName == "" {
		return nil
	}
	return &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			networkName: {},
		},
	}
}
