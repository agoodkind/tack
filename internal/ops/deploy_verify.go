// deploy_verify.go is the post-deploy correctness gate: the app and
// audit-consumer containers must run the images the deploy named. A mismatch
// means compose kept or rolled back to a stale image, and the check fails
// loudly so the operator notices before walking away. Images are built by the
// repository's build workflow and rolled by the configs deploy; this command
// only reads the daemon it runs against.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
)

// deployVerifyTarget pairs a rolled container with the image it must run.
type deployVerifyTarget struct {
	Container string
	Image     string
}

// deployVerifyTargets names the containers a deploy rolls, in the compose
// project's naming, with the image each must run: the registry namespace and
// image names the build workflow pushes, at the operator's explicit tag or the
// tag the rendered environment pins.
func deployVerifyTargets(cfg *config.Config, explicitTag string) []deployVerifyTarget {
	tag := strings.TrimSpace(explicitTag)
	if tag == "" {
		tag = strings.TrimSpace(cfg.DeployImageTag)
	}
	registry := strings.TrimSuffix(strings.TrimSpace(cfg.DeployRegistry), "/")
	return []deployVerifyTarget{
		{Container: "tack-app-1", Image: registry + "/tack-server:" + tag},
		{Container: "tack-audit-consumer-1", Image: registry + "/tack-audit-consumer:" + tag},
	}
}

// runDeployVerify reads each expected image's registry digest from the daemon
// and compares it with the digest the matching container runs.
func runDeployVerify(ctx context.Context, cfg *config.Config, sink clispec.ResultSink, explicitTag string) error {
	const command = "ops deploy verify"
	targets := deployVerifyTargets(cfg, explicitTag)
	cli, err := newDockerClient(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", command, err)
	}
	defer func() { _ = cli.Close() }()

	lines := make([]string, 0, len(targets))
	for _, target := range targets {
		slog.DebugContext(ctx, "ops.deploy.verify.target",
			slog.String("container", target.Container), slog.String("image_ref", target.Image))
		expected, err := inspectImageDigest(ctx, cli, target.Image)
		if err != nil {
			return fmt.Errorf("%s: %w", command, err)
		}
		if expected == "" {
			err := fmt.Errorf("image %s carries no registry digest on this daemon", target.Image)
			slog.ErrorContext(ctx, "ops.deploy.verify.no_expected", slog.String("err", err.Error()))
			return fmt.Errorf("%s: %w", command, err)
		}
		actual, err := containerImageDigest(ctx, cli, target.Container)
		if err != nil {
			return fmt.Errorf("%s: %w", command, err)
		}
		if err := compareDigests(target.Container, expected, actual); err != nil {
			slog.ErrorContext(ctx, "ops.deploy.verify.mismatch",
				slog.String("container", target.Container),
				slog.String("expected", expected),
				slog.String("actual", actual),
				slog.String("err", err.Error()))
			return fmt.Errorf("%s: %w", command, err)
		}
		lines = append(lines, target.Container+" runs "+target.Image+" ("+expected+")")
	}
	slog.InfoContext(ctx, "ops.deploy.verify.completed", slog.Int("containers", len(targets)))
	if err := sink.WriteText(ctx, strings.Join(lines, "\n")); err != nil {
		slog.ErrorContext(ctx, "ops.deploy.verify.write_result_failed", slog.String("err", err.Error()))
		return fmt.Errorf("%s: write result: %w", command, err)
	}
	return nil
}

// inspectImageDigest returns the first registry digest of the named image,
// the content-addressable manifest digest a registry returns on push. An empty
// string means the image was never pulled from or pushed to a registry.
func inspectImageDigest(ctx context.Context, cli *client.Client, ref string) (string, error) {
	insp, err := cli.ImageInspect(ctx, ref)
	if err != nil {
		slog.ErrorContext(ctx, "ops.deploy.image.inspect_failed",
			slog.String("ref", ref), slog.String("err", err.Error()))
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

// containerImageDigest returns the manifest digest the named container is
// actually running. The inspect response's Image field holds either an
// `image:tag` reference or a `sha256:...` digest depending on how the
// container was started; this normalizes to digest form by re-inspecting the
// image when needed.
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
