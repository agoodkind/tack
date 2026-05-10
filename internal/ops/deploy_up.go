// Package ops: deploy_up.go runs `docker compose up -d` on the remote
// daemon via the env-configured docker context. The Docker SDK does not
// cover compose directly; the CLI is the canonical entry point. After up,
// poll service liveness for up to dctx.timeout so a failed deploy aborts
// before the operator walks away.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

func runDeployUp(ctx context.Context, dctx *deployContext) error {
	dctx.log.InfoContext(ctx, "ops.deploy.up.start",
		slog.String("compose_file", dctx.composeFile),
		slog.String("docker_context", dctx.dockerCtx),
	)
	args := []string{
		"compose",
		"-f", dctx.composeFile,
		"up", "-d", "--no-build",
		"app", "audit-consumer",
	}
	out, err := runDockerCmd(ctx, dctx.log, args...)
	if err != nil {
		dctx.log.ErrorContext(ctx, "ops.deploy.up.compose_failed",
			slog.String("err", err.Error()),
			slog.String("output", strings.TrimSpace(out)))
		return fmt.Errorf("compose up: %w (output: %s)", err, strings.TrimSpace(out))
	}
	dctx.log.InfoContext(ctx, "ops.deploy.up.compose_output",
		slog.String("stdout", strings.TrimSpace(out)),
	)
	cli, err := newDockerClient(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	err = waitServicesRunning(ctx, cli, dctx,
		[]string{"tack-app-1", "tack-audit-consumer-1"})
	if err != nil {
		return err
	}
	dctx.log.InfoContext(ctx, "ops.deploy.up.completed")
	return nil
}

// waitServicesRunning polls `docker inspect` for each named container until
// every one is running. Healthchecks may be absent, so the gate is
// State.Running. The poll bails after the deploy timeout so a stuck
// container surfaces fast. The deadline is enforced via [context.WithTimeout]
// rather than wall-clock bookkeeping so the deploy code does not call
// [time.Now] directly.
func waitServicesRunning(
	ctx context.Context,
	cli *client.Client,
	dctx *deployContext,
	containers []string,
) error {
	waitCtx, cancel := context.WithTimeout(ctx, dctx.timeout)
	defer cancel()
	for _, name := range containers {
		err := waitOneRunning(waitCtx, cli, dctx, name)
		if err != nil {
			return err
		}
	}
	dctx.log.InfoContext(ctx, "ops.deploy.up.containers_running",
		slog.Int("count", len(containers)))
	return nil
}

func waitOneRunning(
	ctx context.Context,
	cli *client.Client,
	dctx *deployContext,
	name string,
) error {
	for {
		insp, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
		if err == nil && insp.Container.State != nil && insp.Container.State.Running {
			dctx.log.DebugContext(ctx, "ops.deploy.up.container_running",
				slog.String("name", name))
			return nil
		}
		select {
		case <-ctx.Done():
			cerr := ctx.Err()
			err = fmt.Errorf("container %s did not enter running state before timeout: %w",
				name, cerr)
			dctx.log.ErrorContext(ctx, "ops.deploy.up.container_timeout",
				slog.String("name", name), slog.String("err", err.Error()))
			return err
		case <-time.After(2 * time.Second):
		}
	}
}
