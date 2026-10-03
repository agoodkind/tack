// deploy_verify_index.go is the given-digest half of `ops deploy verify`: with
// the expected multi-platform index digests the build workflow published, the
// command compares each running container's image index descriptor with the
// given digest and never with a value the daemon derives from a local tag.

package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	// ContainerdSnapshotterDriverType is the DriverStatus driver-type value of the
	// containerd image store, the only store that records the index digest
	// in the image descriptor.
	ContainerdSnapshotterDriverType = "io.containerd.snapshotter.v1"
	// ImageStoreDriverTypeKey is the DriverStatus key of the image store driver type.
	ImageStoreDriverTypeKey = "driver-type"
	// dockerManifestListMediaType is the Docker multi-platform manifest list.
	dockerManifestListMediaType = "application/vnd.docker.distribution.manifest.list.v2+json"
)

// indexDigestPattern is the only accepted form of a given index digest.
var indexDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// deployIndexDigests maps each verified container to the index digest it must
// run. A nil map selects the local tag comparison.
type deployIndexDigests map[string]string

// parseDeployIndexDigests checks the two digest flags. Both empty selects the
// local tag comparison; one without the other, or a value outside the
// sha256 index digest form, is refused before the daemon is read.
func parseDeployIndexDigests(ctx context.Context, server, consumer string) (deployIndexDigests, error) {
	if server == "" && consumer == "" {
		return nil, nil
	}
	var problems []string
	if server == "" || consumer == "" {
		problems = append(problems, "--tack-server-digest and --tack-audit-consumer-digest must be given together")
	}
	for flag, value := range map[string]string{"--tack-server-digest": server, "--tack-audit-consumer-digest": consumer} {
		if value != "" && !indexDigestPattern.MatchString(value) {
			problems = append(problems, fmt.Sprintf("%s %q is not sha256: followed by 64 lowercase hex digits", flag, value))
		}
	}
	if len(problems) > 0 {
		err := errors.New("ops deploy verify: " + strings.Join(problems, "; "))
		slog.ErrorContext(ctx, "ops.deploy.verify.digest_flags_invalid", slog.String("err", err.Error()))
		return nil, err
	}
	return deployIndexDigests{appContainer: server, auditConsumerContainer: consumer}, nil
}

// imageStoreDriverType returns the DriverStatus driver-type entry, or an empty
// string when the daemon reports none.
func imageStoreDriverType(status [][2]string) string {
	for _, entry := range status {
		if entry[0] == ImageStoreDriverTypeKey {
			return entry[1]
		}
	}
	return ""
}

// requireContainerdImageStore refuses every image store other than the
// containerd snapshotter store.
func requireContainerdImageStore(ctx context.Context, status [][2]string) error {
	driverType := imageStoreDriverType(status)
	if driverType == ContainerdSnapshotterDriverType {
		return nil
	}
	err := fmt.Errorf("unsupported image store: driver-type %q, want %q", driverType, ContainerdSnapshotterDriverType)
	slog.ErrorContext(ctx, "ops.deploy.verify.image_store_unsupported", slog.String("err", err.Error()))
	return err
}

// indexReading is what the daemon reports for one container's image.
type indexReading struct {
	container      string
	driverType     string
	containerImage string
	image          image.InspectResponse
}

// requireIndexDescriptor compares the given digest with the image index
// descriptor digest, as exact strings. The image ID and RepoDigests are
// printed on failure and never compared.
func requireIndexDescriptor(ctx context.Context, given string, reading indexReading) error {
	container, inspected := reading.container, reading.image
	descriptor := inspected.Descriptor
	var reason string
	switch {
	case descriptor == nil:
		reason = "the image has no descriptor"
	case descriptor.MediaType != ocispec.MediaTypeImageIndex && descriptor.MediaType != dockerManifestListMediaType:
		reason = fmt.Sprintf("descriptor media type %q is not an image index", descriptor.MediaType)
	case descriptor.Digest.String() != given:
		reason = "descriptor digest differs from the given digest"
	default:
		return nil
	}
	descriptorDigest, mediaType := "", ""
	if descriptor != nil {
		descriptorDigest, mediaType = descriptor.Digest.String(), descriptor.MediaType
	}
	err := fmt.Errorf("deploy verify: %s on %s (given=%s driver-type=%s container.Image=%s descriptor.digest=%q descriptor.mediaType=%q image.Id=%s repoDigests=%v)",
		reason, container, given, reading.driverType, reading.containerImage, descriptorDigest, mediaType, inspected.ID, inspected.RepoDigests)
	slog.ErrorContext(ctx, "ops.deploy.verify.mismatch", slog.String("container", container), slog.String("err", err.Error()))
	return err
}

// verifyIndexDigests reads the image store once, then each container's image
// by the image ID the container runs, and compares its index descriptor with
// the given digest. It returns one result line per container.
func verifyIndexDigests(ctx context.Context, cli *client.Client, targets []deployVerifyTarget, digests deployIndexDigests) ([]string, error) {
	info, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "ops.deploy.verify.info_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read the daemon image store: %w", err)
	}
	driverType := imageStoreDriverType(info.Info.DriverStatus)
	if err := requireContainerdImageStore(ctx, info.Info.DriverStatus); err != nil {
		return nil, err
	}
	lines := make([]string, 0, len(targets))
	for _, target := range targets {
		given := digests[target.Container]
		inspectedContainer, err := cli.ContainerInspect(ctx, target.Container, client.ContainerInspectOptions{})
		if err != nil {
			slog.ErrorContext(ctx, "ops.deploy.verify.container_inspect_failed",
				slog.String("name", target.Container), slog.String("err", err.Error()))
			return nil, fmt.Errorf("container inspect %s: %w", target.Container, err)
		}
		imageID := strings.TrimSpace(inspectedContainer.Container.Image)
		inspectedImage, err := cli.ImageInspect(ctx, imageID)
		if err != nil {
			slog.ErrorContext(ctx, "ops.deploy.verify.image_inspect_failed",
				slog.String("ref", imageID), slog.String("err", err.Error()))
			return nil, fmt.Errorf("image inspect %s of container %s: %w", imageID, target.Container, err)
		}
		reading := indexReading{
			container: target.Container, driverType: driverType, containerImage: imageID, image: inspectedImage.InspectResponse,
		}
		if err := requireIndexDescriptor(ctx, given, reading); err != nil {
			return nil, err
		}
		lines = append(lines, target.Container+" runs index "+given+" (given digest)")
	}
	return lines, nil
}
