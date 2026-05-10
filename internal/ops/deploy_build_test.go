// Package ops: deploy_build_test.go covers the small pure helpers in the
// build/push paths. The Docker SDK calls themselves are integration-gated
// and live behind DEPLOY_TEST_INTEGRATION=1.

package ops

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	registrytypes "github.com/moby/moby/api/types/registry"
)

func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestSplitNullTerminatedHandlesEmptyAndDoubleSep(t *testing.T) {
	in := []byte("a\x00b\x00\x00c\x00")
	got := splitNullTerminated(in)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("len: got %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("idx %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestBuildArgsFromVersionPopulatesAllFields(t *testing.T) {
	args, err := buildArgsFromVersion(context.Background(), "abc1234")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, name := range []string{"COMMIT", "BUILD_TIME", "TAG", "DIRTY"} {
		v, ok := args[name]
		if !ok || v == nil {
			t.Fatalf("expected %s in build args", name)
		}
		if *v == "" {
			t.Fatalf("expected %s non-empty", name)
		}
	}
	if *args["COMMIT"] != "abc1234" {
		t.Fatalf("COMMIT mismatch: got %q", *args["COMMIT"])
	}
}

func TestEncodeRegistryAuthRoundTrip(t *testing.T) {
	header, err := encodeRegistryAuth("alice", "secret-token", "ghcr.io/agoodkind/tack")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	raw, err := base64.URLEncoding.DecodeString(header)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	var got registrytypes.AuthConfig
	err = json.Unmarshal(raw, &got)
	if err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if got.Username != "alice" {
		t.Fatalf("Username: got %q", got.Username)
	}
	if got.Password != "secret-token" {
		t.Fatalf("Password: got %q", got.Password)
	}
	if got.ServerAddress != "ghcr.io" {
		t.Fatalf("ServerAddress: expected stripped to host, got %q", got.ServerAddress)
	}
}

func TestEncodePullAuthEmptyCreds(t *testing.T) {
	got, err := encodePullAuth("", "", "ghcr.io/anything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty header for empty creds, got %q", got)
	}
}

func TestStreamBuildOutputErrorAborts(t *testing.T) {
	body := bytes.NewBufferString(`{"stream":"Step 1/5"}` + "\n" +
		`{"error":"COPY failed: file not found"}` + "\n" +
		`{"stream":"never reached"}` + "\n")
	err := streamBuildOutput(context.Background(), nopLogger(), body)
	if err == nil {
		t.Fatal("expected error for build error line")
	}
	if !strings.Contains(err.Error(), "COPY failed") {
		t.Fatalf("expected error message in wrapper, got: %v", err)
	}
}

func TestDrainProgressStreamErrorAborts(t *testing.T) {
	body := bytes.NewBufferString(`{"status":"Pushing"}` + "\n" +
		`{"error":"unauthorized"}` + "\n")
	err := drainProgressStream(context.Background(), nopLogger(), body, "test.event")
	if err == nil {
		t.Fatal("expected error for unauthorized push")
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("expected unauthorized in error, got: %v", err)
	}
}

func TestWriteTarFromPathsEmitsKnownFile(t *testing.T) {
	// writeTarFromPaths resolves paths relative to cwd. We create a hermetic
	// fixture in a temp dir and chdir there so the test is not tied to the
	// repo root.
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/go.mod", []byte("module example.com/test\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	var buf bytes.Buffer
	if err := writeTarFromPaths(context.Background(), &buf, []string{"go.mod"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tr := tar.NewReader(&buf)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("tar Next: %v", err)
	}
	if hdr.Name != "go.mod" {
		t.Fatalf("expected go.mod, got %q", hdr.Name)
	}
	if hdr.Typeflag != tar.TypeReg {
		t.Fatalf("expected regular file, got %d", hdr.Typeflag)
	}
}
