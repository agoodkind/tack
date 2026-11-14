// Package ops manages the container runtime through the Docker SDK.
// client.FromEnv reads DOCKER_HOST, DOCKER_CERT_PATH, and DOCKER_TLS_VERIFY.
// Commands use the guest daemon through the socket tack-ops mounts.

package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// newDockerClient uses Docker environment settings and logs connection failures.
func newDockerClient(ctx context.Context) (*client.Client, error) {
	slog.DebugContext(ctx, "ops.docker.client.connect", slog.String("scope", "env"))
	cli, err := client.New(client.FromEnv)
	if err != nil {
		slog.ErrorContext(ctx, "ops.docker.client.connect_failed", slog.String("err", err.Error()))

		return nil, fmt.Errorf("docker client: %w", err)
	}

	return cli, nil
}

// execResult records one exec's stdout, stderr, and exit code.
type execResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ensureImage pulls the image when inspection fails. ContainerCreate does not
// pull missing images, including on fresh hosts or after image cleanup.
// ImagePull uses anonymous access for the public alpine and foundationdb images.
func ensureImage(ctx context.Context, cli *client.Client, log *slog.Logger, image string) error {
	_, err := cli.ImageInspect(ctx, image)
	if err == nil {
		return nil
	}

	body, pullErr := cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if pullErr != nil {
		log.ErrorContext(
			ctx,
			"ops.docker.run_once.pull_failed",
			slog.String("image", image),
			slog.String("err", pullErr.Error()),
		)

		return fmt.Errorf("pull one-shot image %q: %w", image, pullErr)
	}

	drainErr := drainProgressStream(ctx, log, body, "ops.docker.run_once.pull")
	_ = body.Close()
	if drainErr != nil {
		log.ErrorContext(
			ctx,
			"ops.docker.run_once.pull_stream_failed",
			slog.String("image", image),
			slog.String("err", drainErr.Error()),
		)

		return fmt.Errorf("drain pull stream for one-shot image %q: %w", image, drainErr)
	}

	log.InfoContext(ctx, "ops.docker.run_once.image_pulled", slog.String("image", image))

	return nil
}

// runOneShotOptions configures a single-use container, like docker run --rm.
type runOneShotOptions struct {
	Image         string
	Network       string   // Empty selects the default network.
	Entrypoint    []string // This optional value overrides the image entrypoint.
	Cmd           []string
	Env           []string          // Entries use KEY=VAL form.
	Binds         []string          // Entries use host:container[:ro] form.
	ExtraHosts    []string          // Entries use hostname:ip form for /etc/hosts.
	Name          string            // This optional value sets the container name.
	User          string            `exhaustruct:"optional"` // Empty uses the image user; otherwise use uid:gid.
	PrivateFiles  map[string][]byte `exhaustruct:"optional"`
	PrivateOutput bool              `exhaustruct:"optional"`
}

// runOneShot creates and runs a container, captures its output, and removes it.
// Deferred cleanup uses a non-cancellable context to remove the scratch
// container even after SIGINT interrupts a backup.
func runOneShot(
	ctx context.Context,
	cli *client.Client,
	log *slog.Logger,
	opts runOneShotOptions,
) (execResult, error) {
	err := ensureImage(ctx, cli, log, opts.Image)
	if err != nil {
		log.ErrorContext(
			ctx,
			"ops.docker.run_once.ensure_image_failed",
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
		User:         opts.User,
		AttachStdout: true,
		AttachStderr: true,
	}
	hostCfg := oneShotHostConfig(opts)
	created, err := cli.ContainerCreate(
		ctx,
		client.ContainerCreateOptions{
			Config:           cfg,
			HostConfig:       hostCfg,
			NetworkingConfig: netMode(opts.Network),
			Name:             opts.Name,
		},
	)
	if err != nil {
		log.ErrorContext(
			ctx,
			"ops.docker.run_once.create_failed",
			slog.String("image", opts.Image),
			slog.String("err", err.Error()),
		)

		return execResult{}, fmt.Errorf("create one-shot container: %w", err)
	}
	defer cleanupOneShot(ctx, cli, created.ID)
	if opts.PrivateOutput || len(opts.PrivateFiles) > 0 {
		return runPrivateOneShot(ctx, cli, created.ID, opts.PrivateFiles)
	}

	_, err = cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{})
	if err != nil {
		log.ErrorContext(
			ctx,
			"ops.docker.run_once.start_failed",
			slog.String("image", opts.Image),
			slog.String("err", err.Error()),
		)

		return execResult{}, fmt.Errorf("start one-shot container: %w", err)
	}

	waitRes := cli.ContainerWait(
		ctx,
		created.ID,
		client.ContainerWaitOptions{
			Condition: container.WaitConditionNotRunning,
		},
	)
	var exitCode int
	select {
	case res := <-waitRes.Result:
		exitCode = int(res.StatusCode)
	case waitErr := <-waitRes.Error:
		log.ErrorContext(
			ctx,
			"ops.docker.run_once.wait_failed",
			slog.String("image", opts.Image),
			slog.String("err", waitErr.Error()),
		)

		return execResult{}, fmt.Errorf("wait one-shot container: %w", waitErr)
	case <-ctx.Done():
		return execResult{}, fmt.Errorf("wait one-shot container: %w", ctx.Err())
	}

	logsRdr, err := cli.ContainerLogs(
		ctx,
		created.ID,
		client.ContainerLogsOptions{
			ShowStdout: true,
			ShowStderr: true,
		},
	)
	if err != nil {
		log.ErrorContext(
			ctx,
			"ops.docker.run_once.logs_failed",
			slog.String("image", opts.Image),
			slog.String("err", err.Error()),
		)

		return execResult{}, fmt.Errorf("logs one-shot container: %w", err)
	}
	defer logsRdr.Close()
	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, logsRdr)
	if err != nil && !errors.Is(err, io.EOF) {
		log.ErrorContext(
			ctx,
			"ops.docker.run_once.stream_failed",
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

// containerExec runs cmd in an existing container and returns its output and
// exit code. Use containerExecStreaming for large output to avoid buffering it.
//
// Cancellation closes the attached connection to end the read. The Engine API
// does not support killing an exec; the command continues until it exits or
// its container stops. Restore drill probes run in scratch containers that
// cleanupRestoreDrill force-removes, killing their processes. Commands in live
// containers (provisioning's fdbcli and seed, and export's tar) finish on their own.
func containerExec(
	ctx context.Context,
	cli *client.Client,
	containerID string,
	cmd []string,
) (execResult, error) {
	created, err := cli.ExecCreate(
		ctx,
		containerID,
		client.ExecCreateOptions{
			Cmd:          cmd,
			AttachStdout: true,
			AttachStderr: true,
		},
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"ops.docker.exec.create_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)

		return execResult{}, fmt.Errorf("exec create: %w", err)
	}

	att, err := cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		slog.ErrorContext(
			ctx,
			"ops.docker.exec.attach_failed",
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
		slog.ErrorContext(
			ctx,
			"ops.docker.exec.stream_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)

		return execResult{}, fmt.Errorf("exec stream: %w", err)
	}

	insp, err := cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		slog.ErrorContext(
			ctx,
			"ops.docker.exec.inspect_failed",
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

// containerExecStreaming writes stdout directly to the supplied writer and
// returns stderr and the exit code. Backup dumps can exceed container memory;
// buffering a ysql_dump used 3.78 GB VmRSS before an out-of-memory kill on
// 2026-05-10.
func containerExecStreaming(
	ctx context.Context,
	cli *client.Client,
	containerID string,
	cmd []string,
	envVars []string,
	stdout io.Writer,
) (exitCode int, stderr string, err error) {
	created, err := cli.ExecCreate(
		ctx,
		containerID,
		client.ExecCreateOptions{
			Cmd:          cmd,
			Env:          envVars,
			AttachStdout: true,
			AttachStderr: true,
		},
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"ops.docker.exec_stream.create_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)

		return 0, "", fmt.Errorf("exec create: %w", err)
	}

	att, err := cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		slog.ErrorContext(
			ctx,
			"ops.docker.exec_stream.attach_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)

		return 0, "", fmt.Errorf("exec attach: %w", err)
	}
	defer att.Close()
	// Cancellation closes the stream, not the command, as in containerExec.
	stopClosing := context.AfterFunc(ctx, att.Close)
	defer stopClosing()
	var stderrBuf bytes.Buffer
	_, err = stdcopy.StdCopy(stdout, &stderrBuf, att.Reader)
	if err != nil && !errors.Is(err, io.EOF) {
		slog.ErrorContext(
			ctx,
			"ops.docker.exec_stream.copy_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)

		return 0, stderrBuf.String(), fmt.Errorf("exec stream copy: %w", err)
	}

	insp, err := cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		slog.ErrorContext(
			ctx,
			"ops.docker.exec_stream.inspect_failed",
			slog.String("container", containerID),
			slog.String("err", err.Error()),
		)

		return 0, stderrBuf.String(), fmt.Errorf("exec inspect: %w", err)
	}

	return insp.ExitCode, stderrBuf.String(), nil
}

// removeContainerForce removes a container and its anonymous volumes during
// deferred cleanup. The yugabyte and foundationdb images declare volumes that
// guest pruning does not remove (TACK-498). This does not remove named volumes.
func removeContainerForce(ctx context.Context, cli *client.Client, name string) {
	_, _ = cli.ContainerRemove(
		ctx,
		name,
		client.ContainerRemoveOptions{Force: true, RemoveVolumes: true},
	)
}

// netMode constructs a NetworkingConfig for the specified Docker network.
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
