// deploy_verify.go checks that app and audit-consumer containers run the
// images specified by deployment. It reports a mismatch if Compose retains
// or restores a stale image. The build workflow creates images, and the configs
// deployment installs them. This command only reads the Docker daemon.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

// runDeployVerify compares container images with supplied index digests or
// registry digests recorded by the daemon for expected tags. It returns printed
// results and an external audit target for each checked container.
func runDeployVerify(
	ctx context.Context,
	cfg *config.Config,
	explicitTag string,
	services []string,
	digests deployIndexDigests,
) (audit.OperatorReadOutput, error) {
	targets := deployVerifyTargets(cfg, explicitTag, services)
	cli, err := newDockerClient(ctx)
	if err != nil {
		return audit.OperatorReadOutput{}, deployVerifyFailure(ctx, err)
	}

	defer func() { _ = cli.Close() }()

	replicas, err := serviceReplicas(ctx, cli, cfg.DeployComposeProject, targets)
	if err != nil {
		return audit.OperatorReadOutput{}, deployVerifyFailure(ctx, err)
	}

	var lines []string
	if digests != nil {
		lines, err = verifyIndexDigests(ctx, cli, replicas, digests)
	} else {
		lines, err = verifyLocalTagDigests(ctx, cli, replicas)
	}

	if err != nil {
		return audit.OperatorReadOutput{}, deployVerifyFailure(ctx, err)
	}

	slog.InfoContext(ctx, "ops.deploy.verify.completed", slog.Int("containers", len(replicas)))

	return audit.OperatorReadOutput{
		Payload: []byte(strings.Join(lines, "\n")),
		Format:  deployVerifyPayloadFormat,
		Targets: replicaTargets(replicas),
	}, nil
}

// deployVerifyFailure logs err and adds the command name.
func deployVerifyFailure(ctx context.Context, err error) error {
	slog.ErrorContext(ctx, "ops.deploy.verify.failed", slog.String("err", err.Error()))
	return fmt.Errorf("ops deploy verify: %w", err)
}

// verifyLocalTagDigests compares each replica's digest with its expected image's
// registry digest from the daemon.
func verifyLocalTagDigests(
	ctx context.Context,
	cli *client.Client,
	replicas []serviceReplica,
) ([]string, error) {
	lines := make([]string, 0, len(replicas))
	for _, replica := range replicas {
		slog.DebugContext(
			ctx,
			"ops.deploy.verify.target",
			slog.String("container", replica.Container),
			slog.String("image_ref", replica.Image),
		)
		expected, err := inspectImageDigest(ctx, cli, replica.Image)
		if err != nil {
			return nil, err
		}

		if expected == "" {
			err := fmt.Errorf(
				"this daemon does not record a registry digest for image %s",
				replica.Image,
			)
			slog.ErrorContext(ctx, "ops.deploy.verify.no_expected", slog.String("err", err.Error()))

			return nil, err
		}

		actual, err := containerImageDigest(ctx, cli, replica.Container)
		if err != nil {
			return nil, err
		}

		if err := compareDigests(replica.Container, expected, actual); err != nil {
			slog.ErrorContext(
				ctx,
				"ops.deploy.verify.mismatch",
				slog.String("container", replica.Container),
				slog.String("expected", expected),
				slog.String("actual", actual),
				slog.String("err", err.Error()),
			)

			return nil, err
		}

		lines = append(lines, replica.Container+" runs "+replica.Image+" ("+expected+", local tag)")
	}

	return lines, nil
}

// inspectImageDigest returns the image's first registry manifest digest.
// An empty result indicates that the daemon does not record a registry digest.
func inspectImageDigest(ctx context.Context, cli *client.Client, ref string) (string, error) {
	insp, err := cli.ImageInspect(ctx, ref)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"ops.deploy.image.inspect_failed",
			slog.String("ref", ref),
			slog.String("err", err.Error()),
		)

		return "", fmt.Errorf("inspect %s: %w", ref, err)
	}

	for _, rd := range insp.RepoDigests {
		_, after, ok := strings.Cut(rd, "@")
		if ok {
			return after, nil
		}
	}

	return "", nil
}

// containerImageDigest returns the running container's manifest digest. The
// inspect response's Image field contains an image:tag reference or sha256
// digest. The function inspects the image again when it needs a digest for a tag.
func containerImageDigest(ctx context.Context, cli *client.Client, name string) (string, error) {
	insp, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		slog.ErrorContext(
			ctx,
			"ops.deploy.verify.container_inspect_failed",
			slog.String("name", name),
			slog.String("err", err.Error()),
		)

		return "", fmt.Errorf("container inspect %s: %w", name, err)
	}

	imageRef := strings.TrimSpace(insp.Container.Image)
	if imageRef == "" {
		return "", fmt.Errorf("container %s has empty image reference", name)
	}

	if strings.HasPrefix(imageRef, "sha256:") {
		return imageRef, nil
	}

	digest, err := inspectImageDigest(ctx, cli, imageRef)
	if err != nil {
		return "", err
	}

	if digest != "" {
		return digest, nil
	}

	imgInsp, err := cli.ImageInspect(ctx, imageRef)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"ops.deploy.verify.image_inspect_failed",
			slog.String("ref", imageRef),
			slog.String("err", err.Error()),
		)

		return "", fmt.Errorf("image inspect %s: %w", imageRef, err)
	}

	return imgInsp.ID, nil
}

// compareDigests compares canonical digests after trimming algorithm prefixes
// and normalizing hex case. Either input may use an algorithm:hex form.
func compareDigests(container, expected, actual string) error {
	exp := normalizeDigest(expected)
	act := normalizeDigest(actual)
	if exp == "" || act == "" {
		return fmt.Errorf(
			"deploy verify: empty digest (container=%s expected=%q actual=%q)",
			container,
			expected,
			actual,
		)
	}

	if exp != act {
		return fmt.Errorf(
			"deploy verify: digest mismatch on %s (expected=%s actual=%s)",
			container,
			expected,
			actual,
		)
	}

	return nil
}

// normalizeDigest trims the optional algorithm prefix and lowercases hex for
// case-insensitive comparison with a bare digest.
func normalizeDigest(d string) string {
	d = strings.TrimSpace(d)
	if d == "" {
		return ""
	}

	if i := strings.LastIndex(d, "@"); i >= 0 {
		d = d[i+1:]
	}

	_, after, ok := strings.Cut(d, ":")
	if ok {
		return strings.ToLower(after)
	}

	return strings.ToLower(d)
}
