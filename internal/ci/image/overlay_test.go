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

const decoyText = "decoy\n"

type overlayScenario struct {
	name   string
	fdb    func(t *testing.T) []v1.Layer
	yb     func(t *testing.T) []v1.Layer
	edit   func(t *testing.T, root string)
	want   string
	failed bool
}

func plainFDB(t *testing.T) []v1.Layer {
	return []v1.Layer{
		imagetest.Layer(t, regular(fdbPath, "stale\n")),
		imagetest.Layer(t, regular("var/fdb/scripts/.wh.fdb.bash", "")),
		imagetest.Layer(t, regular(fdbPath, upstreamText)),
	}
}

func chainedYB(t *testing.T) []v1.Layer {
	return []v1.Layer{
		imagetest.Layer(t, regular("home/yugabyte/lib/real-yugabyted", upstreamText), symlink("home/yugabyte/tools/yugabyted", "../lib/real-yugabyted")),
		imagetest.Layer(t, symlink("home/yugabyte/bin", "tools")),
	}
}

func singleYB(t *testing.T, entries ...imagetest.Entry) func(*testing.T) []v1.Layer {
	return func(t *testing.T) []v1.Layer { return []v1.Layer{imagetest.Layer(t, entries...)} }
}

func TestOverlayDriftCheckAgainstRegistryImages(t *testing.T) {
	for _, test := range []overlayScenario{
		{name: "whiteout and link chain pass", fdb: plainFDB, yb: chainedYB, want: "Overlay drift check passed"},
		{name: "oversized unrelated same-name file passes", want: "Overlay drift check passed", yb: chainedYB, fdb: func(t *testing.T) []v1.Layer {
			return append(plainFDB(t), imagetest.Layer(t, regular("opt/other/fdb.bash", strings.Repeat("x", 17<<20))))
		}},
		{name: "oversized requested file", failed: true, want: "size limit", yb: chainedYB, fdb: func(t *testing.T) []v1.Layer {
			return []v1.Layer{imagetest.Layer(t, regular(fdbPath, strings.Repeat("x", 17<<20)))}
		}},
		{name: "hard link passes", fdb: singleYB(t, regular("var/fdb/original", upstreamText), imagetest.Entry{Name: fdbPath, Type: tar.TypeLink, Linkname: "var/fdb/original"}), yb: chainedYB, want: "Overlay drift check passed"},
		{name: "whiteout removes target", failed: true, want: "could not extract", yb: chainedYB, fdb: func(t *testing.T) []v1.Layer {
			return []v1.Layer{imagetest.Layer(t, regular(fdbPath, upstreamText)), imagetest.Layer(t, regular("var/fdb/scripts/.wh.fdb.bash", ""))}
		}},
		{name: "link escaping the image root is rejected", failed: true, want: "not a regular file", fdb: plainFDB, yb: singleYB(t, symlink(ybPath, "../../../../../etc/passwd"))},
		{name: "link cycle", failed: true, want: "hop limit", fdb: plainFDB, yb: singleYB(t, symlink(ybPath, "loop"), symlink("home/yugabyte/bin/loop", "yugabyted"))},
		{name: "hash mismatch", failed: true, want: "changed (sha256", fdb: plainFDB, yb: chainedYB, edit: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "fdb-overlay/fdb.bash.upstream.sha256"), strings.Repeat("0", 64)+"\n")
		}},
		{name: "patch does not apply", failed: true, want: "does not apply cleanly", fdb: plainFDB, yb: chainedYB, edit: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "yugabyte-overlay/yugabyted.patch"), strings.Replace(validPatch, "-b", "-z", 1))
		}},
		{name: "overlay differs from patched upstream", failed: true, want: "does not equal upstream", fdb: plainFDB, yb: chainedYB, edit: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "fdb-overlay/fdb.bash"), "a\nB\nc\nextra\n")
		}},
		{name: "patch changes two files", failed: true, want: "changes 2 files", fdb: plainFDB, yb: chainedYB, edit: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "fdb-overlay/fdb.bash.patch"), validPatch+strings.ReplaceAll(validPatch, "x", "y"))
		}},
		{name: "missing vendored file", failed: true, want: "cannot access vendored file", fdb: plainFDB, yb: chainedYB, edit: func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "yugabyte-overlay/yugabyted.patch")); err != nil {
				t.Fatalf("remove patch: %v", err)
			}
		}},
	} {
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
