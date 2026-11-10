package manifest

import (
	"fmt"
	"log/slog"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// collectAddenda fetches each source image or index and returns its index
// entries in source order, preserving each child descriptor.
func collectAddenda(
	sources []name.Digest,
	options []remote.Option,
) ([]mutate.IndexAddendum, error) {
	var addenda []mutate.IndexAddendum

	for _, source := range sources {
		descriptor, err := remote.Get(source, options...)
		if err != nil {
			slog.Error(
				"manifest.source_get_failed",
				slog.String("source", source.String()),
				slog.Any("error", err),
			)

			return nil, fmt.Errorf("get image source %s: %w", source, err)
		}

		children, err := addendaFor(descriptor)
		if err != nil {
			slog.Error(
				"manifest.source_read_failed",
				slog.String("source", source.String()),
				slog.Any("error", err),
			)

			return nil, fmt.Errorf("read image source %s: %w", source, err)
		}

		addenda = append(addenda, children...)
	}

	return addenda, nil
}

// addendaFor returns the index entries that one source contributes.
func addendaFor(descriptor *remote.Descriptor) ([]mutate.IndexAddendum, error) {
	if descriptor.MediaType.IsIndex() {
		return indexChildren(descriptor)
	}

	digest := descriptor.Digest.String()

	image, err := descriptor.Image()
	if err != nil {
		slog.Error(
			"manifest.image_read_failed",
			slog.String("digest", digest),
			slog.Any("error", err),
		)

		return nil, fmt.Errorf("read image %s: %w", digest, err)
	}

	configuration, err := image.ConfigFile()
	if err != nil {
		slog.Error(
			"manifest.image_configuration_failed",
			slog.String("digest", digest),
			slog.Any("error", err),
		)

		return nil, fmt.Errorf("read image configuration %s: %w", digest, err)
	}

	entry := descriptor.Descriptor
	entry.Platform = &v1.Platform{
		OS:           configuration.OS,
		Architecture: configuration.Architecture,
		Variant:      configuration.Variant,
		OSVersion:    configuration.OSVersion,
	}

	return []mutate.IndexAddendum{{Add: image, Descriptor: entry}}, nil
}

// indexChildren returns every child of a source index with its original
// descriptor.
func indexChildren(descriptor *remote.Descriptor) ([]mutate.IndexAddendum, error) {
	digest := descriptor.Digest.String()

	source, err := descriptor.ImageIndex()
	if err != nil {
		slog.Error(
			"manifest.index_read_failed",
			slog.String("digest", digest),
			slog.Any("error", err),
		)

		return nil, fmt.Errorf("read index %s: %w", digest, err)
	}

	manifest, err := source.IndexManifest()
	if err != nil {
		slog.Error(
			"manifest.index_manifest_failed",
			slog.String("digest", digest),
			slog.Any("error", err),
		)

		return nil, fmt.Errorf("read index manifest %s: %w", digest, err)
	}

	children := make([]mutate.IndexAddendum, 0, len(manifest.Manifests))

	for _, child := range manifest.Manifests {
		var appendable mutate.Appendable
		if child.MediaType.IsIndex() {
			appendable, err = source.ImageIndex(child.Digest)
		} else {
			appendable, err = source.Image(child.Digest)
		}

		if err != nil {
			slog.Error(
				"manifest.child_read_failed",
				slog.String("index", digest),
				slog.String("child", child.Digest.String()),
				slog.Any("error", err),
			)

			return nil, fmt.Errorf("read index child %s: %w", child.Digest, err)
		}

		children = append(children, mutate.IndexAddendum{Add: appendable, Descriptor: child})
	}

	return children, nil
}
