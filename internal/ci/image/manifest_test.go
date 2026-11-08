package image_test

import (
	"archive/tar"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"goodkind.io/tack/internal/ci/image/imagetest"
)

const (
	manifestOwner = "owner"
	manifestImage = "tack-server"
)

// manifestFixture is a registry with native digests and the files that the manifest command reads.
type manifestFixture struct {
	host       string
	tagFile    string
	digestDir  string
	tags       []string
	wantChilds []v1.Descriptor
}

// pushNative writes an index with a platform child and an attestation child, as the native build does.
func pushNative(t *testing.T, host, architecture string) (string, []v1.Descriptor) {
	t.Helper()
	platform := v1.Platform{OS: "linux", Architecture: architecture}
	platformImage := imagetest.Image(t, platform, imagetest.Layer(t, imagetest.Entry{Name: "binary-" + architecture, Type: tar.TypeReg, Content: architecture}))
	attestation := imagetest.Image(t, v1.Platform{OS: "unknown", Architecture: "unknown"}, imagetest.Layer(t, imagetest.Entry{Name: "attestation-" + architecture, Type: tar.TypeReg, Content: architecture}))
	platformDigest, err := platformImage.Digest()
	if err != nil {
		t.Fatalf("digest platform image: %v", err)
	}
	index := imagetest.PushIndex(t, host+"/"+manifestOwner+"/"+manifestImage+":source-"+architecture,
		mutate.IndexAddendum{Add: platformImage, Descriptor: v1.Descriptor{Platform: &platform}},
		mutate.IndexAddendum{Add: attestation, Descriptor: v1.Descriptor{
			Platform: &v1.Platform{OS: "unknown", Architecture: "unknown"},
			Annotations: map[string]string{
				"vnd.docker.reference.type":   "attestation-manifest",
				"vnd.docker.reference.digest": platformDigest.String(),
			},
		}},
	)
	digest, err := index.Digest()
	if err != nil {
		t.Fatalf("digest index: %v", err)
	}
	manifest, err := index.IndexManifest()
	if err != nil {
		t.Fatalf("read index manifest: %v", err)
	}
	return digest.String(), manifest.Manifests
}

func newManifestFixture(t *testing.T) manifestFixture {
	t.Helper()
	host := imagetest.StartRegistry(t)
	directory := t.TempDir()
	fixture := manifestFixture{host: host, digestDir: filepath.Join(directory, "digests"), tagFile: filepath.Join(directory, "image-tags")}
	if err := os.Mkdir(fixture.digestDir, 0o755); err != nil {
		t.Fatalf("create digest directory: %v", err)
	}
	for _, architecture := range []string{"amd64", "arm64"} {
		digest, children := pushNative(t, host, architecture)
		fixture.wantChilds = append(fixture.wantChilds, children...)
		if err := os.WriteFile(filepath.Join(fixture.digestDir, architecture+".digest"), []byte(digest+"\n"), 0o644); err != nil {
			t.Fatalf("write digest file: %v", err)
		}
	}
	base := host + "/" + manifestOwner + "/" + manifestImage
	fixture.tags = []string{base + ":abc1234", base + ":latest"}
	writeFixtureTags(t, fixture, strings.Join(fixture.tags, "\n")+"\n")
	return fixture
}

func writeFixtureTags(t *testing.T, fixture manifestFixture, content string) {
	t.Helper()
	if err := os.WriteFile(fixture.tagFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write tag file: %v", err)
	}
}

func (fixture manifestFixture) environment() []string {
	return []string{
		"REGISTRY=" + fixture.host, "IMAGE_OWNER=" + manifestOwner, "IMAGE_NAME=" + manifestImage,
		"IMAGE_TAG_FILE=" + fixture.tagFile, "DIGEST_DIRECTORY=" + fixture.digestDir,
		"GITHUB_ACTOR=builder", "GITHUB_TOKEN=test-token",
	}
}

func TestManifestPublishesEveryChildUnderEveryTag(t *testing.T) {
	fixture := newManifestFixture(t)
	output, err := runWithArguments(t, fixture.environment(), "manifest")
	if err != nil {
		t.Fatalf("err = %v, output = %s", err, output)
	}
	for _, tag := range fixture.tags {
		reference, err := name.ParseReference(tag)
		if err != nil {
			t.Fatalf("parse %s: %v", tag, err)
		}
		index, err := remote.Index(reference)
		if err != nil {
			t.Fatalf("read published index %s: %v", tag, err)
		}
		mediaType, err := index.MediaType()
		if err != nil || mediaType != types.OCIImageIndex {
			t.Fatalf("%s media type = %s, err = %v", tag, mediaType, err)
		}
		manifest, err := index.IndexManifest()
		if err != nil {
			t.Fatalf("read index manifest %s: %v", tag, err)
		}
		if !reflect.DeepEqual(manifest.Manifests, fixture.wantChilds) {
			t.Fatalf("%s children = %+v, want %+v", tag, manifest.Manifests, fixture.wantChilds)
		}
		for _, child := range manifest.Manifests {
			if _, err := index.Image(child.Digest); err != nil {
				t.Fatalf("%s child %s is not readable: %v", tag, child.Digest, err)
			}
		}
	}
}

func TestManifestRefusesBadInputBeforePublishing(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, fixture manifestFixture, environment []string) []string
	}{
		{"uppercase digest", func(t *testing.T, fixture manifestFixture, environment []string) []string {
			writeDigest(t, fixture, "arm64", "sha256:"+strings.Repeat("A", 64))
			return environment
		}},
		{"missing digest file", func(t *testing.T, fixture manifestFixture, environment []string) []string {
			if err := os.Remove(filepath.Join(fixture.digestDir, "arm64.digest")); err != nil {
				t.Fatalf("remove digest: %v", err)
			}
			return environment
		}},
		{"absent source image", func(t *testing.T, fixture manifestFixture, environment []string) []string {
			writeDigest(t, fixture, "arm64", "sha256:"+strings.Repeat("b", 64))
			return environment
		}},
		{"empty tag file", func(t *testing.T, fixture manifestFixture, environment []string) []string {
			writeFixtureTags(t, fixture, "\n\n")
			return environment
		}},
		{"empty token", func(t *testing.T, _ manifestFixture, environment []string) []string {
			return append(slices.DeleteFunc(environment, func(item string) bool { return strings.HasPrefix(item, "GITHUB_TOKEN=") }), "GITHUB_TOKEN=")
		}},
		{"missing actor", func(t *testing.T, _ manifestFixture, environment []string) []string {
			return slices.DeleteFunc(environment, func(item string) bool { return strings.HasPrefix(item, "GITHUB_ACTOR=") })
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newManifestFixture(t)
			environment := test.mutate(t, fixture, fixture.environment())
			output, err := runWithArguments(t, environment, "manifest")
			if err == nil {
				t.Fatalf("expected failure, output = %s", output)
			}
			if strings.Count(output, "ERROR ") != 1 || !strings.Contains(output, "ci-image.failed") {
				t.Fatalf("expected one boundary log, output = %s", output)
			}
			if strings.Contains(output, "test-token") {
				t.Fatalf("output contains the token: %s", output)
			}
			for _, tag := range fixture.tags {
				reference, parseErr := name.ParseReference(tag)
				if parseErr != nil {
					t.Fatalf("parse %s: %v", tag, parseErr)
				}
				if _, headErr := remote.Head(reference); headErr == nil {
					t.Fatalf("tag %s exists after a refused publication", tag)
				}
			}
		})
	}
}

func writeDigest(t *testing.T, fixture manifestFixture, architecture, digest string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.digestDir, architecture+".digest"), []byte(digest+"\n"), 0o644); err != nil {
		t.Fatalf("write digest file: %v", err)
	}
}
