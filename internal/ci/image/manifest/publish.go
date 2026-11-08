package manifest

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Publish combines the amd64 and arm64 source images into one OCI index and writes it under every tag.
// It validates every input and reads every source before it writes any tag.
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
			return fmt.Errorf("write image index %s: %w", tag, err)
		}
	}
	return nil
}
