// Package ops: deploy_pull.go fetches the freshly-pushed image on the
// remote daemon. In registry mode this is a real `docker pull` against
// ghcr.io. In offline mode the previous step (deploy_push) already loaded
// the image into the remote daemon, so pull is a no-op.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/moby/moby/client"
)

func runDeployPull(ctx context.Context, dctx *deployContext) error {
	dctx.log.InfoContext(ctx, "ops.deploy.pull.dispatch",
		slog.String("mode", dctx.mode))
	if dctx.mode == deployModeOffline {
		dctx.log.InfoContext(ctx, "ops.deploy.pull.skip",
			slog.String("reason", "offline mode already loaded the image on the remote"),
		)
		return nil
	}
	return runDeployPullRegistry(ctx, dctx)
}

func runDeployPullRegistry(ctx context.Context, dctx *deployContext) error {
	dctx.log.InfoContext(ctx, "ops.deploy.pull.start",
		slog.String("image_ref", dctx.imageRef),
		slog.String("docker_context", dctx.dockerCtx),
	)
	cli, err := newDockerClient(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	authHeader, err := encodePullAuth(dctx.ghcrUser, dctx.ghcrToken, dctx.registry)
	if err != nil {
		dctx.log.ErrorContext(ctx, "ops.deploy.pull.auth_encode_failed",
			slog.String("err", err.Error()))
		return err
	}
	for _, ref := range []string{dctx.imageRef, dctx.latestRef} {
		body, perr := cli.ImagePull(ctx, ref, client.ImagePullOptions{RegistryAuth: authHeader})
		if perr != nil {
			dctx.log.ErrorContext(ctx, "ops.deploy.pull.ref_failed",
				slog.String("ref", ref), slog.String("err", perr.Error()))
			return fmt.Errorf("image pull %s: %w", ref, perr)
		}
		err = drainProgressStream(ctx, dctx.log, body, "ops.deploy.pull.stream")
		_ = body.Close()
		if err != nil {
			return err
		}
	}
	dctx.log.InfoContext(ctx, "ops.deploy.pull.completed",
		slog.String("image_ref", dctx.imageRef),
	)
	return nil
}

// encodePullAuth mirrors encodeRegistryAuth but returns an empty string when
// no creds are set so anonymous pulls of public images keep working. The
// deploy context constructor enforces that creds exist in registry mode, so
// the empty-creds branch is reached only by the offline path that bypasses
// pull entirely. Marshal-shape: the caller logs the wrap.
func encodePullAuth(user, token, registryHost string) (string, error) {
	if user == "" && token == "" {
		return "", nil
	}
	host, _, _ := strings.Cut(registryHost, "/")
	return marshalAuthEnvelope(user, token, host)
}
