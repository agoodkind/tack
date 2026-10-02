package ops

import (
	"strings"
	"testing"

	"github.com/moby/moby/api/types/image"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	givenIndexDigest = "sha256:04fa9f9300000000000000000000000000000000000000000000000000000001"
	otherIndexDigest = "sha256:5a02a7d200000000000000000000000000000000000000000000000000000002"
	upperIndexDigest = "sha256:04FA9F9300000000000000000000000000000000000000000000000000000001"
	sha512SameHex    = "sha512:04fa9f9300000000000000000000000000000000000000000000000000000001"
)

// TestParseDeployIndexDigestsRequiresBothInIndexForm requires both flags or
// neither, each as sha256 and 64 lowercase hex digits.
func TestParseDeployIndexDigestsRequiresBothInIndexForm(t *testing.T) {
	if digests, err := parseDeployIndexDigests(t.Context(), "", ""); err != nil || digests != nil {
		t.Fatalf("no flags = %v, %v, want the local tag comparison", digests, err)
	}
	digests, err := parseDeployIndexDigests(t.Context(), givenIndexDigest, otherIndexDigest)
	if err != nil || digests["tack-app-1"] != givenIndexDigest || digests["tack-audit-consumer-1"] != otherIndexDigest {
		t.Fatalf("both flags = %v, %v", digests, err)
	}
	refused := [][2]string{
		{givenIndexDigest, ""},
		{"", otherIndexDigest},
		{upperIndexDigest, otherIndexDigest},
		{sha512SameHex, otherIndexDigest},
		{"04fa9f93", otherIndexDigest},
	}
	for _, flags := range refused {
		if _, err := parseDeployIndexDigests(t.Context(), flags[0], flags[1]); err == nil {
			t.Errorf("flags %q were accepted", flags)
		}
	}
}

// TestRequireContainerdImageStoreReadsDriverType accepts only the containerd
// snapshotter driver-type.
func TestRequireContainerdImageStoreReadsDriverType(t *testing.T) {
	if err := requireContainerdImageStore(t.Context(), [][2]string{{"driver-type", containerdSnapshotterDriverType}}); err != nil {
		t.Fatalf("containerd store refused: %v", err)
	}
	refused := [][][2]string{nil, {{"Backing Filesystem", "extfs"}}, {{"driver-type", "overlay2"}}}
	for _, status := range refused {
		if err := requireContainerdImageStore(t.Context(), status); err == nil {
			t.Errorf("driver status %v was accepted", status)
		}
	}
}

// TestRequireIndexDescriptorComparesOnlyTheIndexDigest gives the image ID and
// a RepoDigest the given digest in every case. Only an index descriptor with
// exactly the given digest passes.
func TestRequireIndexDescriptorComparesOnlyTheIndexDigest(t *testing.T) {
	descriptor := func(mediaType string, digest ocispec.Descriptor) *ocispec.Descriptor {
		digest.MediaType = mediaType
		return &digest
	}
	cases := []struct {
		name       string
		descriptor *ocispec.Descriptor
		pass       bool
	}{
		{"OCI index with the given digest", descriptor(ocispec.MediaTypeImageIndex, ocispec.Descriptor{Digest: givenIndexDigest}), true},
		{"Docker manifest list with the given digest", descriptor(dockerManifestListMediaType, ocispec.Descriptor{Digest: givenIndexDigest}), true},
		{"no descriptor", nil, false},
		{"single-manifest descriptor", descriptor(ocispec.MediaTypeImageManifest, ocispec.Descriptor{Digest: givenIndexDigest}), false},
		{"index with a different digest", descriptor(ocispec.MediaTypeImageIndex, ocispec.Descriptor{Digest: otherIndexDigest}), false},
		{"index with the same hex in uppercase", descriptor(ocispec.MediaTypeImageIndex, ocispec.Descriptor{Digest: upperIndexDigest}), false},
		{"index with the same hex under sha512", descriptor(ocispec.MediaTypeImageIndex, ocispec.Descriptor{Digest: sha512SameHex}), false},
	}
	for _, tc := range cases {
		inspected := image.InspectResponse{ID: givenIndexDigest, RepoDigests: []string{"registry.test/tack-server@" + givenIndexDigest}, Descriptor: tc.descriptor}
		reading := indexReading{container: "tack-app-1", driverType: containerdSnapshotterDriverType, containerImage: givenIndexDigest, image: inspected}
		err := requireIndexDescriptor(t.Context(), givenIndexDigest, reading)
		if tc.pass && err != nil {
			t.Errorf("%s: refused: %v", tc.name, err)
		}
		if !tc.pass && (err == nil || !strings.Contains(err.Error(), "tack-app-1")) {
			t.Errorf("%s: error = %v, want a refusal for tack-app-1", tc.name, err)
		}
	}
}
