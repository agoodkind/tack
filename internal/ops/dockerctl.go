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
	"github.com/moby/moby/api/types/container"
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

// ensureImage guarantees the named image is present on the target daemon
// before a one-shot container is created from it. runOneShot calls
// cli.ContainerCreate directly, which does not pull on a cache miss, so a
// helper image absent from the host (alpine on a fresh QA host, or anything
// after image GC) would fail create with "No such image". A successful
// ImageInspect means the image is already local and no pull is needed; any
// inspect error is treated as not-present, which is safe because a successful
// anonymous pull then makes create work. alpine and foundationdb are public,
// so ImagePull runs without RegistryAuth.
func ensureImage(ctx context.Context, cli *client.Client, log *slog.Logger, image string) error {
	_, err := cli.ImageInspect(ctx, image)
	if err == nil {
		return nil
	}
	body, pullErr := cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if pullErr != nil {
		log.ErrorContext(ctx, "ops.docker.run_once.pull_failed",
			slog.String("image", image),
			slog.String("err", pullErr.Error()),
		)
		return fmt.Errorf("pull one-shot image %q: %w", image, pullErr)
	}
	drainErr := drainProgressStream(ctx, log, body, "ops.docker.run_once.pull")
	_ = body.Close()
	if drainErr != nil {
		log.ErrorContext(ctx, "ops.docker.run_once.pull_stream_failed",
			slog.String("image", image),
			slog.String("err", drainErr.Error()),
		)
		return fmt.Errorf("drain pull stream for one-shot image %q: %w", image, drainErr)
	}
	log.InfoContext(ctx, "ops.docker.run_once.image_pulled",
		slog.String("image", image),
	)
	return nil
}

// runOneShotOptions configures a single-use docker container created, run to
// completion, captured, and removed. Equivalent to `docker run --rm` but
// without shelling out to the docker CLI.
type runOneShotOptions struct {
	Image      string
	Network    string   // optional; empty joins default
	Entrypoint []string // optional override
	Cmd        []string
	Env        []string // KEY=VAL form
	Binds      []string // host:container[:ro]
	ExtraHosts []string // optional /etc/hosts entries, "hostname:ip" form
	Name       string   // optional explicit container name
}

// runOneShot creates a container with the given options, starts it, waits for
// it to exit, captures stdout/stderr, and force-removes it. Replaces shell-outs
// to `docker run --rm <image> <cmd>` per TACK-264. Removal happens via deferred
// teardown using a non-cancellable context so SIGINT during a backup still
// cleans up the scratch container.
func runOneShot(
	ctx context.Context,
	cli *client.Client,
	log *slog.Logger,
	opts runOneShotOptions,
) (execResult, error) {
	err := ensureImage(ctx, cli, log, opts.Image)
	if err != nil {
		log.ErrorContext(ctx, "ops.docker.run_once.ensure_image_failed",
			slog.String("image", opts.Image),
			slog.String("err", err.Error()),
		)
		return execResult{}, fmt.Errorf("ensure one-shot image %q: %w", opts.Image, err)
	}
	cfg := &container.Config{
		Image:        opts.Image,
		Entrypoint:   opts.Entrypoint,
		Cmd:          opts.Cmd,
		Env:          opts.Env,
		AttachStdout: true,
		AttachStderr: true,
	}
	hostCfg := &container.HostConfig{
		Binds:      opts.Binds,
		ExtraHosts: opts.ExtraHosts,
	}
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           cfg,
		HostConfig:       hostCfg,
		NetworkingConfig: netMode(opts.Network),
		Name:             opts.Name,
	})
	if err != nil {
		log.ErrorContext(ctx, "ops.docker.run_once.create_failed",
			slog.String("image", opts.Image),
			slog.String("err", err.Error()),
		)
		return execResult{}, fmt.Errorf("create one-shot container: %w", err)
	}
	defer func() {
		teardown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = cli.ContainerRemove(teardown, created.ID, client.ContainerRemoveOptions{Force: true})
	}()

	_, err = cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{})
	if err != nil {
		log.ErrorContext(ctx, "ops.docker.run_once.start_failed",
			slog.String("image", opts.Image),
			slog.String("err", err.Error()),
		)
		return execResult{}, fmt.Errorf("start one-shot container: %w", err)
	}

	waitRes := cli.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{
		Condition: container.WaitConditionNotRunning,
	})
	var exitCode int
	select {
	case res := <-waitRes.Result:
		exitCode = int(res.StatusCode)
	case waitErr := <-waitRes.Error:
		log.ErrorContext(ctx, "ops.docker.run_once.wait_failed",
			slog.String("image", opts.Image),
			slog.String("err", waitErr.Error()),
		)
		return execResult{}, fmt.Errorf("wait one-shot container: %w", waitErr)
	case <-ctx.Done():
		return execResult{}, fmt.Errorf("wait one-shot container: %w", ctx.Err())
	}

	logsRdr, err := cli.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		log.ErrorContext(ctx, "ops.docker.run_once.logs_failed",
			slog.String("image", opts.Image),
			slog.String("err", err.Error()),
		)
		return execResult{}, fmt.Errorf("logs one-shot container: %w", err)
	}
	defer logsRdr.Close()
	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, logsRdr)
	if err != nil && !errors.Is(err, io.EOF) {
		log.ErrorContext(ctx, "ops.docker.run_once.stream_failed",
			slog.String("image", opts.Image),
			slog.String("err", err.Error()),
		)
		return execResult{}, fmt.Errorf("read one-shot logs: %w", err)
	}

	return execResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}

// containerExec runs cmd inside an existing container and returns the
// captured streams plus exit code. Equivalent to `docker exec` but typed
// and stream-safe. Use [containerExecStreaming] when stdout may be large
// (multi-MB+) to avoid buffering it in memory.
//
// The exec's streams close when ctx ends. The attach is a hijacked connection
// the SDK does not tie to the context, so without this a command that never
// exits would hold the read past any deadline the caller set.
func containerExec(
	ctx context.Context,
	cli *client.Client,
	containerID string,
	cmd []string,
) (execResult, error) {
	created, err := cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		Cmd:          cmd,
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
	stopClosing := context.AfterFunc(ctx, att.Close)
	defer stopClosing()
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

// containerExecStreaming runs cmd inside an existing container and streams
// stdout directly to the caller-provided writer instead of buffering in memory.
// Stderr is captured to a string (small; error messages only). Returns the
// exit code and stderr. Used by backup paths that produce multi-GB dumps —
// the in-memory buffering pattern of [containerExec] would OOM the orchestrator
// container otherwise (verified by 2026-05-10 observability: yugabyte ysql_dump
// pushed VmRSS to 3.78 GB before being OOM-killed).
func containerExecStreaming(
	ctx context.Context,
	cli *client.Client,
	containerID string,
	cmd []string,
	envVars []string,
	stdout io.Writer,
) (exitCode int, stderr string, err error) {
	created, err := cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		Cmd:          cmd,
		Env:          envVars,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		slog.ErrorContext(ctx, "ops.docker.exec_stream.create_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)
		return 0, "", fmt.Errorf("exec create: %w", err)
	}
	att, err := cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "ops.docker.exec_stream.attach_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)
		return 0, "", fmt.Errorf("exec attach: %w", err)
	}
	defer att.Close()
	// As in containerExec: the hijacked attach outlives the context on its
	// own, so the stream is closed when ctx ends.
	stopClosing := context.AfterFunc(ctx, att.Close)
	defer stopClosing()
	var stderrBuf bytes.Buffer
	_, err = stdcopy.StdCopy(stdout, &stderrBuf, att.Reader)
	if err != nil && !errors.Is(err, io.EOF) {
		slog.ErrorContext(ctx, "ops.docker.exec_stream.copy_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)
		return 0, stderrBuf.String(), fmt.Errorf("exec stream copy: %w", err)
	}
	insp, err := cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "ops.docker.exec_stream.inspect_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)
		return 0, stderrBuf.String(), fmt.Errorf("exec inspect: %w", err)
	}
	return insp.ExitCode, stderrBuf.String(), nil
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
