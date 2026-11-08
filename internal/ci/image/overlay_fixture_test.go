package image_test

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"

	"goodkind.io/tack/internal/ci/image/imagetest"
)

const (
	upstreamText = "a\nb\nc\n"
	overlayText  = "a\nB\nc\n"
	validPatch   = "--- x.upstream\n+++ x\n@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n"
	fdbPath      = "var/fdb/scripts/fdb.bash"
	ybPath       = "home/yugabyte/bin/yugabyted"
)

func regular(name, content string) imagetest.Entry {
	return imagetest.Entry{Name: name, Type: tar.TypeReg, Content: content, Linkname: ""}
}

func symlink(name, target string) imagetest.Entry {
	return imagetest.Entry{Name: name, Type: tar.TypeSymlink, Content: "", Linkname: target}
}

// pushPlatforms writes an index with an amd64 image and an arm64 decoy
// image.
func pushPlatforms(t *testing.T, tag string, amd64 []v1.Layer, arm64 []v1.Layer) {
	t.Helper()

	amd64Platform := v1.Platform{OS: linuxPlatform, Architecture: "amd64"}
	arm64Platform := v1.Platform{OS: linuxPlatform, Architecture: "arm64"}

	imagetest.PushIndex(t, tag,
		mutate.IndexAddendum{
			Add:      imagetest.Image(t, arm64Platform, arm64...),
			Platform: &arm64Platform,
		},
		mutate.IndexAddendum{
			Add:      imagetest.Image(t, amd64Platform, amd64...),
			Platform: &amd64Platform,
		},
	)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	err := os.MkdirAll(filepath.Dir(path), testDirMode)
	if err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}

	writeTestFile(t, path, content)
}

// writeRepository writes the Makefile, Compose file, and vendored files
// that the check reads.
func writeRepository(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	sum := sha256.Sum256([]byte(upstreamText))
	recorded := hex.EncodeToString(sum[:]) + "\n"

	writeFile(t, filepath.Join(root, "Makefile"), "FDB_VERSION ?= 1.2.3\n")
	writeFile(
		t,
		filepath.Join(root, "docker-compose.yml"),
		"x-yugabyte-image: &yugabyte_image yugabytedb/yugabyte:test\n",
	)

	for _, overlay := range []string{"fdb-overlay/fdb.bash", "yugabyte-overlay/yugabyted"} {
		writeFile(t, filepath.Join(root, overlay), overlayText)
		writeFile(t, filepath.Join(root, overlay+".upstream.sha256"), recorded)
		writeFile(t, filepath.Join(root, overlay+".patch"), validPatch)
	}

	return root
}

func overlayDriftCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "./ci-image", "overlay-drift")
}

func runOverlayCheck(t *testing.T, root, host string) (string, error) {
	t.Helper()

	command := imagetest.Prepare(t, root, overlayDriftCommand)
	command.Env = []string{"UPSTREAM_REGISTRY=" + host}

	output, err := command.CombinedOutput()

	return string(output), err
}
