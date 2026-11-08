package overlay

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// extractError reports that the pinned upstream file could not be read
// from the image.
type extractError struct {
	item entry
	err  error
}

func (e *extractError) Error() string {
	return "FAIL: could not extract " + e.item.imagePath + " from " + e.item.image +
		": " + e.err.Error()
}

func (e *extractError) Unwrap() error {
	return e.err
}

// upstreamFile reads the pinned file from the linux/amd64 image with
// anonymous access. A nonempty registry prefixes the image name with that
// host.
func upstreamFile(ctx context.Context, registry string, item entry) ([]byte, error) {
	address := item.image
	if registry != "" {
		address = registry + "/" + item.image
	}

	reference, err := name.ParseReference(address)
	if err != nil {
		slog.Error("overlay.image_parse_failed", slog.String("image", address), slog.Any("error", err))

		return nil, fmt.Errorf("parse image %s: %w", address, err)
	}

	image, err := remote.Image(reference,
		remote.WithContext(ctx),
		remote.WithAuth(authn.Anonymous),
		remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "amd64"}),
	)
	if err != nil {
		slog.Error("overlay.image_pull_failed", slog.String("image", address), slog.Any("error", err))

		return nil, fmt.Errorf("pull image %s: %w", address, err)
	}

	return readFile(ctx, image, item.imagePath)
}
