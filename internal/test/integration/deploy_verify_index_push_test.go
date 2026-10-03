package integration

import (
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// dockerManifestListMediaType is the Docker media type of a multi-platform
// image list. ops deploy verify accepts it beside the OCI image index.
const dockerManifestListMediaType = "application/vnd.docker.distribution.manifest.list.v2+json"

// pushMultiPlatformIndex pushes one linux/amd64 and one linux/arm64 image with
// the same layer, then an OCI index of both under tag, and returns the index
// digest.
func (r localRegistry) pushMultiPlatformIndex(t *testing.T, repository, tag, marker string) string {
	t.Helper()
	return r.pushMultiPlatformList(t, repository, tag, marker, ocispec.MediaTypeImageIndex)
}

// pushMultiPlatformList pushes one linux/amd64 and one linux/arm64 image with
// the same layer, then a list of both with the given media type under tag. It
// returns the digest of the list.
func (r localRegistry) pushMultiPlatformList(t *testing.T, repository, tag, marker, mediaType string) string {
	t.Helper()
	layer, diffID := singleFileLayer(t, marker)
	layerDescriptor := r.pushBlob(t, repository, ocispec.MediaTypeImageLayerGzip, layer)
	manifests := make([]registryDescriptor, 0, 2)
	for _, architecture := range []string{"amd64", "arm64"} {
		descriptor := r.pushImageManifest(t, repository, architecture, layerDescriptor, diffID, "")
		descriptor.Platform = &ocispec.Platform{Architecture: architecture, OS: "linux"}
		manifests = append(manifests, descriptor)
	}
	list := mustMarshal(t, map[string]any{
		"schemaVersion": 2, "mediaType": mediaType, "manifests": manifests,
	})
	pushed := r.pushManifest(t, repository, tag, mediaType, list)
	t.Logf("pushed %s:%s as %s %s", repository, tag, mediaType, pushed.Digest)
	return pushed.Digest
}
