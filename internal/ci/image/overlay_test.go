package image_test

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"goodkind.io/tack/internal/ci/image/imagetest"
)

const (
	decoyText      = "decoy\n"
	successText    = "Overlay drift check passed"
	oversizedBytes = 17 << 20
)

type overlayScenario struct {
	name   string
	fdb    func(t *testing.T) []v1.Layer
	yb     func(t *testing.T) []v1.Layer
	edit   func(t *testing.T, root string)
	want   string
	failed bool
}

func passing(name string, fdb, yb func(t *testing.T) []v1.Layer) overlayScenario {
	return overlayScenario{name: name, fdb: fdb, yb: yb, edit: nil, want: successText, failed: false}
}

func failing(
	name, want string,
	fdb, yb func(t *testing.T) []v1.Layer,
	edit func(t *testing.T, root string),
) overlayScenario {
	return overlayScenario{name: name, fdb: fdb, yb: yb, edit: edit, want: want, failed: true}
}

func plainFDB(t *testing.T) []v1.Layer {
	t.Helper()

	return []v1.Layer{
		imagetest.Layer(t, regular(fdbPath, "stale\n")),
		imagetest.Layer(t, regular("var/fdb/scripts/.wh.fdb.bash", "")),
		imagetest.Layer(t, regular(fdbPath, upstreamText)),
	}
}

func chainedYB(t *testing.T) []v1.Layer {
	t.Helper()

	return []v1.Layer{
		imagetest.Layer(t,
			regular("home/yugabyte/lib/real-yugabyted", upstreamText),
			symlink("home/yugabyte/tools/yugabyted", "../lib/real-yugabyted"),
		),
		imagetest.Layer(t, symlink("home/yugabyte/bin", "tools")),
	}
}

func layersOf(entries ...imagetest.Entry) func(*testing.T) []v1.Layer {
	return func(t *testing.T) []v1.Layer {
		t.Helper()

		return []v1.Layer{imagetest.Layer(t, entries...)}
	}
}

func oversizedUnrelatedFDB(t *testing.T) []v1.Layer {
	t.Helper()

	other := regular("opt/other/fdb.bash", strings.Repeat("x", oversizedBytes))

	return append(plainFDB(t), imagetest.Layer(t, other))
}

func oversizedRequestedFDB(t *testing.T) []v1.Layer {
	t.Helper()

	return []v1.Layer{imagetest.Layer(t, regular(fdbPath, strings.Repeat("x", oversizedBytes)))}
}

func whitedOutFDB(t *testing.T) []v1.Layer {
	t.Helper()

	return []v1.Layer{
		imagetest.Layer(t, regular(fdbPath, upstreamText)),
		imagetest.Layer(t, regular("var/fdb/scripts/.wh.fdb.bash", "")),
	}
}

func replaceFile(relative, content string) func(*testing.T, string) {
	return func(t *testing.T, root string) {
		t.Helper()
		writeFile(t, filepath.Join(root, relative), content)
	}
}

func removeFile(relative string) func(*testing.T, string) {
	return func(t *testing.T, root string) {
		t.Helper()

		err := os.Remove(filepath.Join(root, relative))
		if err != nil {
			t.Fatalf("remove %s: %v", relative, err)
		}
	}
}

func overlayScenarios() []overlayScenario {
	hardLink := imagetest.Entry{
		Name: fdbPath, Type: tar.TypeLink, Content: "", Linkname: "var/fdb/original",
	}
	hashFile := "fdb-overlay/fdb.bash.upstream.sha256"
	ybPatch := "yugabyte-overlay/yugabyted.patch"
	fdbPatch := "fdb-overlay/fdb.bash.patch"

	return []overlayScenario{
		passing("whiteout and link chain pass", plainFDB, chainedYB),
		passing("oversized unrelated same-name file passes", oversizedUnrelatedFDB, chainedYB),
		failing("oversized requested file", "size limit", oversizedRequestedFDB, chainedYB, nil),
		passing("hard link passes", layersOf(regular("var/fdb/original", upstreamText), hardLink), chainedYB),
		failing("whiteout removes target", "could not extract", whitedOutFDB, chainedYB, nil),
		failing(
			"link escaping the image root is rejected", "not a regular file", plainFDB,
			layersOf(symlink(ybPath, "../../../../../etc/passwd")), nil,
		),
		failing(
			"link cycle", "hop limit", plainFDB,
			layersOf(symlink(ybPath, "loop"), symlink("home/yugabyte/bin/loop", "yugabyted")), nil,
		),
		failing(
			"hash mismatch", "changed (sha256", plainFDB, chainedYB,
			replaceFile(hashFile, strings.Repeat("0", 64)+"\n"),
		),
		failing(
			"patch does not apply", "does not apply cleanly", plainFDB, chainedYB,
			replaceFile(ybPatch, strings.Replace(validPatch, "-b", "-z", 1)),
		),
		failing(
			"overlay differs from patched upstream", "does not equal upstream", plainFDB, chainedYB,
			replaceFile("fdb-overlay/fdb.bash", "a\nB\nc\nextra\n"),
		),
		failing(
			"patch changes two files", "changes 2 files", plainFDB, chainedYB,
			replaceFile(fdbPatch, validPatch+strings.ReplaceAll(validPatch, "x", "y")),
		),
		failing(
			"missing vendored file", "cannot access vendored file", plainFDB, chainedYB,
			removeFile(ybPatch),
		),
	}
}

// TestOverlayDriftCheckAgainstRegistryImages checks overlay drift against
// registry images, including file, link, hash, and patch conditions.
func TestOverlayDriftCheckAgainstRegistryImages(t *testing.T) {
	for _, test := range overlayScenarios() {
		t.Run(test.name, func(t *testing.T) {
			host := imagetest.StartRegistry(t)
			decoy := []v1.Layer{imagetest.Layer(t, regular(fdbPath, decoyText), regular(ybPath, decoyText))}

			pushPlatforms(t, host+"/foundationdb/foundationdb:1.2.3", test.fdb(t), decoy)
			pushPlatforms(t, host+"/yugabytedb/yugabyte:test", test.yb(t), decoy)

			root := writeRepository(t)
			if test.edit != nil {
				test.edit(t, root)
			}

			output, err := runOverlayCheck(t, root, host)
			if test.failed && (err == nil || !strings.Contains(output, "TACK-302")) {
				t.Fatalf("expected a drift failure, err = %v, output = %s", err, output)
			}

			if !test.failed && err != nil {
				t.Fatalf("err = %v, output = %s", err, output)
			}

			if !strings.Contains(output, test.want) {
				t.Fatalf("output does not contain %q: %s", test.want, output)
			}
		})
	}
}
