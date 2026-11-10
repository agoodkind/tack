package manifest

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Publish combines the amd64 and arm64 source images into one OCI index.
//
// Publish validates the inputs and reads both sources before writing the
// index under each nonempty tag from IMAGE_TAG_FILE.
//
// DIGEST_DIRECTORY contains these source digest files:
//
//	amd64.digest
//	arm64.digest
func Publish(ctx context.Context) error {
	input, err := readInputs()
	if err != nil {
		return err
	}

	options := []remote.Option{remote.WithContext(ctx), remote.WithAuth(input.auth)}

	addenda, err := collectAddenda(input.sources, options)
	if err != nil {
		return err
	}

	index := mutate.AppendManifests(
		mutate.IndexMediaType(empty.Index, types.OCIImageIndex),
		addenda...,
	)

	for _, tag := range input.tags {
		err = remote.WriteIndex(tag, index, options...)
		if err != nil {
			slog.Error(
				"manifest.index_write_failed",
				slog.String("tag", tag.String()),
				slog.Any("error", err),
			)

			return fmt.Errorf("write image index %s: %w", tag, err)
		}
	}

	return nil
}
