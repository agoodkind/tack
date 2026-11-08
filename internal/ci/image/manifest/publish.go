package manifest

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Publish combines the amd64 and arm64 source images into one OCI index.
// It validates the inputs and reads both sources before writing the index
// under each tag from IMAGE_TAG_FILE.
// For example, the function reads amd64.digest and arm64.digest from
// DIGEST_DIRECTORY and writes the index under each nonempty tag file line.
func Publish(ctx context.Context) error {
	input, err := readInputs()
	if err != nil {
		return err
	}
	options := []remote.Option{remote.WithContext(ctx), remote.WithAuth(input.auth)}
	index, err := combine(input.sources, options)
	if err != nil {
		return err
	}
	for _, tag := range input.tags {
		if err := remote.WriteIndex(tag, index, options...); err != nil {
			slog.Error("manifest.index_write_failed", slog.String("tag", tag.String()), slog.Any("error", err))
			return fmt.Errorf("write image index %s: %w", tag, err)
		}
	}
	return nil
}
