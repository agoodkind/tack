package image_test

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"

	"goodkind.io/tack/internal/ci/image/imagetest"
)

const (
	manifestOwner   = "owner"
	manifestImage   = "tack-server"
	unknownPlatform = "unknown"
	githubAuthKey   = "GITHUB_TOKEN="
	actorPrefix     = "GITHUB_ACTOR="
)

// manifestFixture is a registry with native digests and the files that
// the manifest command reads.
type manifestFixture struct {
	host       string
	tagFile    string
	digestDir  string
	tags       []string
	wantChilds []v1.Descriptor
}

// pushNative writes an index with a platform child and an attestation
// child, as the native build does.
func pushNative(t *testing.T, host, architecture string) (string, []v1.Descriptor) {
	t.Helper()

	platform := v1.Platform{OS: linuxPlatform, Architecture: architecture}
	unknown := v1.Platform{OS: unknownPlatform, Architecture: unknownPlatform}
	platformLayer := imagetest.Layer(t, imagetest.Entry{
		Name: "binary-" + architecture, Type: tar.TypeReg, Content: architecture, Linkname: "",
	})
	attestationLayer := imagetest.Layer(t, imagetest.Entry{
		Name: "attestation-" + architecture, Type: tar.TypeReg, Content: architecture, Linkname: "",
	})
	platformImage := imagetest.Image(t, platform, platformLayer)
	attestation := imagetest.Image(t, unknown, attestationLayer)

	platformDigest, err := platformImage.Digest()
	if err != nil {
		t.Fatalf("digest platform image: %v", err)
	}

	pushed := imagetest.PushIndex(t, host+"/"+manifestOwner+"/"+manifestImage+":source-"+architecture,
		mutate.IndexAddendum{Add: platformImage, Platform: &platform},
		mutate.IndexAddendum{
			Add:      attestation,
			Platform: &unknown,
			Annotations: map[string]string{
				"vnd.docker.reference.type":   "attestation-manifest",
				"vnd.docker.reference.digest": platformDigest.String(),
			},
		},
	)

	return pushed.Digest.String(), pushed.Manifest.Manifests
}

func newManifestFixture(t *testing.T) manifestFixture {
	t.Helper()

	host := imagetest.StartRegistry(t)
	directory := t.TempDir()
	fixture := manifestFixture{
		host:       host,
		tagFile:    filepath.Join(directory, "image-tags"),
		digestDir:  filepath.Join(directory, "digests"),
		tags:       nil,
		wantChilds: nil,
	}

	err := os.Mkdir(fixture.digestDir, testDirMode)
	if err != nil {
		t.Fatalf("create digest directory: %v", err)
	}

	for _, architecture := range []string{"amd64", "arm64"} {
		digest, children := pushNative(t, host, architecture)
		fixture.wantChilds = append(fixture.wantChilds, children...)

		writeDigest(t, fixture, architecture, digest)
	}

	base := host + "/" + manifestOwner + "/" + manifestImage
	fixture.tags = []string{base + ":abc1234", base + ":latest"}
	writeTestFile(t, fixture.tagFile, strings.Join(fixture.tags, "\n")+"\n")

	return fixture
}

func writeDigest(t *testing.T, fixture manifestFixture, architecture, digest string) {
	t.Helper()
	writeTestFile(t, filepath.Join(fixture.digestDir, architecture+".digest"), digest+"\n")
}

func (fixture manifestFixture) environment() []string {
	return []string{
		"REGISTRY=" + fixture.host, "IMAGE_OWNER=" + manifestOwner, "IMAGE_NAME=" + manifestImage,
		"IMAGE_TAG_FILE=" + fixture.tagFile, "DIGEST_DIRECTORY=" + fixture.digestDir,
		actorPrefix + "builder", githubAuthKey + "test-token",
	}
}
