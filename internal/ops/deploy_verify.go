// Package ops: deploy_verify.go is the post-up correctness gate. After
// `docker compose up` returns, the running container's image reference must
// match the digest the push step recorded. Mismatch means compose pulled or
// rolled back to a stale image; the deploy fails loudly so the operator
// notices before walking away.

package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/moby/moby/client"
)

func runDeployVerify(ctx context.Context, dctx *deployContext) error {
	dctx.log.InfoContext(ctx, "ops.deploy.verify.start",
		slog.String("image_ref", dctx.imageRef),
	)
	cli, err := newDockerClient(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	expected, err := resolveExpectedDigest(ctx, cli, dctx)
	if err != nil {
		return err
	}
	if expected == "" {
		err := errors.New("deploy verify: no expected digest available; push step did not record one")
		dctx.log.ErrorContext(ctx, "ops.deploy.verify.no_expected",
			slog.String("err", err.Error()))
		return err
	}
	matched := []string{}
	for _, name := range []string{"tack-app-1", "tack-audit-consumer-1"} {
		actual, derr := containerImageDigest(ctx, cli, name)
		if derr != nil {
			return derr
		}
		cerr := compareDigests(name, expected, actual)
		if cerr != nil {
			dctx.log.ErrorContext(ctx, "ops.deploy.verify.mismatch",
				slog.String("container", name),
				slog.String("expected", expected),
				slog.String("actual", actual),
				slog.String("err", cerr.Error()))
			return cerr
		}
		matched = append(matched, name)
	}
	dctx.log.InfoContext(ctx, "ops.deploy.verify.completed",
		slog.String("digest", expected),
		slog.Any("containers", matched),
	)
	return nil
}

// resolveExpectedDigest returns the manifest digest the push step recorded.
// If no digest was recorded (push was skipped), inspect the image on the
// remote daemon as a fallback so `./server ops deploy verify` works
// standalone.
func resolveExpectedDigest(
	ctx context.Context,
	cli *client.Client,
	dctx *deployContext,
) (string, error) {
	if len(dctx.pushedDigests) > 0 {
		return dctx.pushedDigests[0], nil
	}
	return inspectImageDigest(ctx, cli, dctx.imageRef)
}

// containerImageDigest returns the manifest digest the named container is
// actually running. The InspectResponse's Image field holds either an
// `image:tag` reference or a `sha256:...` digest depending on docker version
// and how the container was started; this function normalizes to digest
// form by re-inspecting the image when needed. Marshal-shape style: error
// log lives at the caller (runDeployVerify) which has full deploy context.
func containerImageDigest(ctx context.Context, cli *client.Client, name string) (string, error) {
	insp, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "ops.deploy.verify.container_inspect_failed",
			slog.String("name", name), slog.String("err", err.Error()))
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
		slog.ErrorContext(ctx, "ops.deploy.verify.image_inspect_failed",
			slog.String("ref", imageRef), slog.String("err", err.Error()))
		return "", fmt.Errorf("image inspect %s: %w", imageRef, err)
	}
	return imgInsp.ID, nil
}

// compareDigests is the assertion. Either side may be `sha256:...` (a
// content digest) or `algorithm:hex` of differing case; canonical forms
// are compared after trimming the algorithm prefix when both have it.
func compareDigests(container, expected, actual string) error {
	exp := normalizeDigest(expected)
	act := normalizeDigest(actual)
	if exp == "" || act == "" {
		return fmt.Errorf("deploy verify: empty digest (container=%s expected=%q actual=%q)",
			container, expected, actual)
	}
	if exp != act {
		return fmt.Errorf(
			"deploy verify: digest mismatch on %s (expected=%s actual=%s)",
			container, expected, actual,
		)
	}
	return nil
}

// normalizeDigest strips the optional algorithm prefix so a `sha256:abc...`
// digest compares equal to the bare hex form. Lowercase the hex to make the
// comparison case-insensitive.
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
