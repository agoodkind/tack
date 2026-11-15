// Index verification compares each running container's image index descriptor
// with the expected build workflow digest, not a daemon-derived tag value.

package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

var errDeployIndexMismatch = errors.New("deployment image index mismatch")

const (
	// ContainerdSnapshotterDriverType identifies the containerd image store,
	// which records the index digest in the image descriptor.
	ContainerdSnapshotterDriverType = "io.containerd.snapshotter.v1"
	// ImageStoreDriverTypeKey specifies the DriverStatus key for
	// the image store driver type.
	ImageStoreDriverTypeKey = "driver-type"
	// dockerManifestListMediaType identifies Docker's manifest list format.
	dockerManifestListMediaType = "application/vnd.docker.distribution.manifest.list.v2+json"
)

// imageStoreDriverType returns the driver-type entry or an empty string.
func imageStoreDriverType(status [][2]string) string {
	for _, entry := range status {
		if entry[0] == ImageStoreDriverTypeKey {
			return entry[1]
		}
	}

	return ""
}

// requireContainerdImageStore rejects stores other than the
// containerd snapshotter.
func requireContainerdImageStore(ctx context.Context, status [][2]string) error {
	driverType := imageStoreDriverType(status)
	if driverType == ContainerdSnapshotterDriverType {
		return nil
	}

	err := fmt.Errorf(
		"unsupported image store: driver-type %q, want %q",
		driverType,
		ContainerdSnapshotterDriverType,
	)
	slog.ErrorContext(
		ctx,
		"ops.deploy.verify.image_store_unsupported",
		slog.String("err", err.Error()),
	)

	return err
}

// indexReading records the daemon's image inspection for one container.
type indexReading struct {
	container      string
	driverType     string
	containerImage string
	image          image.InspectResponse
}

// requireIndexDescriptor compares the expected digest and descriptor digest as
// exact strings. Failure details include the image ID and RepoDigests, but the
// comparison does not use them.
func requireIndexDescriptor(ctx context.Context, given string, imageReading indexReading) error {
	container, inspected := imageReading.container, imageReading.image
	descriptor := inspected.Descriptor
	var reason string
	switch {
	case descriptor == nil:
		reason = "the image does not have a descriptor"
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

	err := fmt.Errorf(
		"deploy verify: %s on %s (given=%s driver-type=%s container.Image=%s "+
			"descriptor.digest=%q descriptor.mediaType=%q image.Id=%s repoDigests=%v): %w",
		reason,
		container,
		given,
		imageReading.driverType,
		imageReading.containerImage,
		descriptorDigest,
		mediaType,
		inspected.ID,
		inspected.RepoDigests,
		errDeployIndexMismatch,
	)
	slog.ErrorContext(
		ctx,
		"ops.deploy.verify.mismatch",
		slog.String("container", container),
		slog.String("err", err.Error()),
	)

	return err
}

// verifyIndexDigests reads the image store once and inspects each image by ID.
// It compares each index descriptor with the expected digest and returns
// one result line per container.
func verifyIndexDigests(
	ctx context.Context,
	cli *client.Client,
	targets []serviceReplica,
	digests deployIndexDigests,
) ([]string, error) {
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
		given := digests[target.Service]
		inspectedContainer, err := cli.ContainerInspect(
			ctx,
			target.Container,
			client.ContainerInspectOptions{},
		)
		if err != nil {
			slog.ErrorContext(
				ctx,
				"ops.deploy.verify.container_inspect_failed",
				slog.String("name", target.Container),
				slog.String("err", err.Error()),
			)

			return nil, fmt.Errorf("container inspect %s: %w", target.Container, err)
		}

		imageID := strings.TrimSpace(inspectedContainer.Container.Image)
		inspectedImage, err := cli.ImageInspect(ctx, imageID)
		if err != nil {
			slog.ErrorContext(
				ctx,
				"ops.deploy.verify.image_inspect_failed",
				slog.String("ref", imageID),
				slog.String("err", err.Error()),
			)

			return nil, fmt.Errorf(
				"image inspect %s of container %s: %w",
				imageID,
				target.Container,
				err,
			)
		}

		imageReading := indexReading{
			container:      target.Container,
			driverType:     driverType,
			containerImage: imageID,
			image:          inspectedImage.InspectResponse,
		}
		if err := requireIndexDescriptor(ctx, given, imageReading); err != nil {
			return nil, err
		}

		lines = append(lines, target.Container+" runs index "+given+" (given digest)")
	}

	return lines, nil
}
