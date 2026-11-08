package manifest

import (
	"fmt"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// combine fetches every source and returns one OCI index that keeps each child descriptor.
func combine(sources []name.Digest, options []remote.Option) (v1.ImageIndex, error) {
	var addenda []mutate.IndexAddendum
	for _, source := range sources {
		descriptor, err := remote.Get(source, options...)
		if err != nil {
			return nil, fmt.Errorf("get image source %s: %w", source, err)
		}
		children, err := addendaFor(descriptor)
		if err != nil {
			return nil, fmt.Errorf("read image source %s: %w", source, err)
		}
		addenda = append(addenda, children...)
	}
	return mutate.AppendManifests(mutate.IndexMediaType(empty.Index, types.OCIImageIndex), addenda...), nil
}

// addendaFor returns the index entries that one source contributes.
func addendaFor(descriptor *remote.Descriptor) ([]mutate.IndexAddendum, error) {
	if descriptor.MediaType.IsIndex() {
		return indexChildren(descriptor)
	}
	image, err := descriptor.Image()
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	configuration, err := image.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("read image configuration: %w", err)
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

// indexChildren returns every child of a source index with its original descriptor.
func indexChildren(descriptor *remote.Descriptor) ([]mutate.IndexAddendum, error) {
	source, err := descriptor.ImageIndex()
	if err != nil {
		return nil, fmt.Errorf("read index: %w", err)
	}
	manifest, err := source.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("read index manifest: %w", err)
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
			return nil, fmt.Errorf("read index child %s: %w", child.Digest, err)
		}
		children = append(children, mutate.IndexAddendum{Add: appendable, Descriptor: child})
	}
	return children, nil
}
