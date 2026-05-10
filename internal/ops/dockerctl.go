// Package ops: dockerctl.go is a thin Docker SDK helper shared by the backup
// and deploy families. The plan calls for laptop-side invocation with
// `DOCKER_CONTEXT=tack` over `ssh://`. The SDK's `client.FromEnv` honors
// `DOCKER_HOST`, `DOCKER_CONTEXT`, and `DOCKER_TLS_VERIFY` automatically;
// this file does not reimplement that.

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

// newDockerClient builds a docker client honoring DOCKER_HOST, DOCKER_CONTEXT,
// and DOCKER_TLS_VERIFY. The deploy family uses this for the remote daemon
// (the `tack` context over ssh://); the backup family uses it for the
// production daemon. Boundary event so dispatch-time failures have a log line
// independent of the wrapped error.
func newDockerClient(ctx context.Context) (*client.Client, error) {
	slog.DebugContext(ctx, "ops.docker.client.connect", slog.String("scope", "env"))
	cli, err := client.New(client.FromEnv)
	if err != nil {
		slog.ErrorContext(ctx, "ops.docker.client.connect_failed",
			slog.String("err", err.Error()))
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return cli, nil
}

// newLocalDockerClient pins to the local daemon by overriding any
// DOCKER_HOST/DOCKER_CONTEXT inherited from the operator env. Used when the
// build half of a deploy must run on the laptop while the up half talks to
// the remote.
func newLocalDockerClient(ctx context.Context) (*client.Client, error) {
	slog.DebugContext(ctx, "ops.docker.client.connect", slog.String("scope", "local"))
	cli, err := client.New(client.WithHost(client.DefaultDockerHost))
	if err != nil {
		slog.ErrorContext(ctx, "ops.docker.client.connect_local_failed",
			slog.String("err", err.Error()))
		return nil, fmt.Errorf("local docker client: %w", err)
	}
	return cli, nil
}

// assertDockerContext verifies the named docker context is registered before
// the backup or deploy family starts. Fails fast: without this, an unset
// context surfaces as a TLS or dial error several layers down. Empty
// contextName is a no-op so callers running against the laptop's default
// daemon are not penalized.
//
// `docker context inspect` is invoked via os/exec because the docker SDK
// does not expose context registration; the CLI owns that file.
func assertDockerContext(ctx context.Context, contextName string) error {
	if contextName == "" {
		slog.DebugContext(ctx, "ops.docker.context.skip", slog.String("reason", "empty name"))
		return nil
	}
	slog.DebugContext(ctx, "ops.docker.context.inspect", slog.String("context", contextName))
	cmd := exec.CommandContext(ctx, "docker", "context", "inspect", contextName)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		slog.ErrorContext(ctx, "ops.docker.context.unconfigured",
			slog.String("context", contextName),
			slog.String("err", err.Error()),
			slog.String("stderr", strings.TrimSpace(stderr.String())))
		return fmt.Errorf("docker context %q not configured: %w (stderr: %s)",
			contextName, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// runDockerCmd runs `docker <args...>` against the env-configured daemon and
// returns combined output. Used by the few operations the SDK does not cover
// cleanly (compose up, fdbbackup). The plan's "no shell scripts" rule allows
// individual typed command invocations; what it forbids is writing new .sh files.
func runDockerCmd(ctx context.Context, log *slog.Logger, args ...string) (string, error) {
	if log == nil {
		log = slog.Default()
	}
	log.DebugContext(ctx, "ops.docker.cmd.start", slog.Any("args", args))
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	log.DebugContext(ctx, "ops.docker.cmd.done",
		slog.Any("args", args),
		slog.Int("output_bytes", len(out)),
	)
	if err != nil {
		log.ErrorContext(ctx, "ops.docker.cmd.failed",
			slog.Any("args", args),
			slog.String("err", err.Error()),
			slog.String("output", strings.TrimSpace(string(out))))
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
		slog.ErrorContext(ctx, "ops.docker.exec.create_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)
		return execResult{}, fmt.Errorf("exec create: %w", err)
	}
	att, err := cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "ops.docker.exec.attach_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)
		return execResult{}, fmt.Errorf("exec attach: %w", err)
	}
	defer att.Close()
	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, att.Reader)
	if err != nil && !errors.Is(err, io.EOF) {
		slog.ErrorContext(ctx, "ops.docker.exec.stream_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)
		return execResult{}, fmt.Errorf("exec stream: %w", err)
	}
	insp, err := cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "ops.docker.exec.inspect_failed",
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
			slog.ErrorContext(ctx, "ops.docker.exec.wait_timeout",
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
