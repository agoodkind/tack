package ops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestNewYBSnapshotManifestAssignsNodePrefixes locks the completeness
// contract's shape: one entry per tablet server, sorted by name, each with the
// run-relative prefix its archive command must fill.
func TestNewYBSnapshotManifestAssignsNodePrefixes(t *testing.T) {
	nowFunc = func() time.Time { return time.Date(2026, 8, 26, 3, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { nowFunc = time.Now })

	manifest := newYBSnapshotManifest("20260826T030000Z", "snap-id", "tack", []string{"yb2", "yb1", "yb3"})

	wantNodes := []ybSnapshotManifestNode{
		{Name: "yb1", Prefix: "nodes/yb1/"},
		{Name: "yb2", Prefix: "nodes/yb2/"},
		{Name: "yb3", Prefix: "nodes/yb3/"},
	}
	if !reflect.DeepEqual(manifest.Nodes, wantNodes) {
		t.Fatalf("nodes mismatch:\n got=%v\nwant=%v", manifest.Nodes, wantNodes)
	}
	if manifest.CreatedAt != "2026-08-26T03:00:00Z" {
		t.Fatalf("created_at = %q, want the pinned clock", manifest.CreatedAt)
	}
	prefix, ok := manifest.nodePrefix("yb2")
	if !ok || prefix != "nodes/yb2/" {
		t.Fatalf("nodePrefix(yb2) = %q, %v", prefix, ok)
	}
	if _, ok := manifest.nodePrefix("yb9"); ok {
		t.Fatal("nodePrefix must report an unlisted node as absent")
	}
}

// TestYBSnapshotManifestFileRoundTrip writes the manifest through the
// production writer and reads it back through the same JSON tags the fetch
// path decodes, so the writer and reader can never disagree on field names.
func TestYBSnapshotManifestFileRoundTrip(t *testing.T) {
	manifest := newYBSnapshotManifest("20260826T030000Z", "snap-1", "tack", []string{"yb1", "yb2"})
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := writeYBSnapshotManifest(context.Background(), path, manifest); err != nil {
		t.Fatalf("writeYBSnapshotManifest: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var decoded ybSnapshotManifest
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, manifest) {
		t.Fatalf("round trip mismatch:\n got=%+v\nwant=%+v", decoded, manifest)
	}
}

// TestYBSnapshotManifestValidateRejectsTraversalRunID proves a manifest whose
// run id carries traversal segments is refused at both trust boundaries: the
// validate method every fetch runs, and the production writer. The run id
// feeds filepath.Join on staging dirs that are recursively removed, so a
// traversal run id must never survive decode or write.
func TestYBSnapshotManifestValidateRejectsTraversalRunID(t *testing.T) {
	for _, runID := range []string{"../../../etc", "20260826T030000Z/../..", "run-1", ""} {
		manifest := newYBSnapshotManifest(runID, "snap-1", "tack", []string{"yb1"})
		if err := manifest.validate(); err == nil {
			t.Errorf("validate accepted run_id %q", runID)
		}
		path := filepath.Join(t.TempDir(), "manifest.json")
		if err := writeYBSnapshotManifest(context.Background(), path, manifest); err == nil {
			t.Errorf("writeYBSnapshotManifest accepted run_id %q", runID)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Errorf("manifest file written despite invalid run_id %q", runID)
		}
	}
}

// TestYBSnapshotManifestValidateRejectsBadNodeNames proves node names carrying
// path separators, traversal segments, or shell metacharacters are refused:
// they feed staged archive file names and the in-container extraction paths.
// A node whose prefix disagrees with the one derived from its name is refused
// too, because the prefix becomes part of the object key the archive uploads
// to.
func TestYBSnapshotManifestValidateRejectsBadNodeNames(t *testing.T) {
	badNames := []string{
		"../yb1", "a..b", "nodes/yb1", `yb\1`,
		"yb1;reboot", "yb$(reboot)", "yb1 yb2", "-yb1", "",
	}
	for _, name := range badNames {
		manifest := ybSnapshotManifest{
			RunID:      "20260826T030000Z",
			SnapshotID: "snap-1",
			Database:   "tack",
			CreatedAt:  "2026-08-26T03:00:00Z",
			Nodes:      []ybSnapshotManifestNode{{Name: name, Prefix: "nodes/" + name + "/"}},
		}
		if err := manifest.validate(); err == nil {
			t.Errorf("validate accepted node name %q", name)
		}
	}

	mismatched := ybSnapshotManifest{
		RunID:      "20260826T030000Z",
		SnapshotID: "snap-1",
		Database:   "tack",
		CreatedAt:  "2026-08-26T03:00:00Z",
		Nodes:      []ybSnapshotManifestNode{{Name: "yb1", Prefix: "nodes/yb2/"}},
	}
	if err := mismatched.validate(); err == nil {
		t.Error("validate accepted a prefix that disagrees with its node name")
	}
}

// TestYBSnapshotManifestValidateAcceptsOrchestratorShape proves the manifests
// the orchestrator actually derives, run-key timestamp run id plus DNS node
// names or an unbracketed IPv6 literal (the hostFromHostPort output for a
// bracketed host:port), pass validation and the production writer.
func TestYBSnapshotManifestValidateAcceptsOrchestratorShape(t *testing.T) {
	manifest := newYBSnapshotManifest("20260826T030000Z", "snap-1", "tack",
		[]string{"yb1", "yb2", "yb3", "3d06:bad:b01::10"})
	if err := manifest.validate(); err != nil {
		t.Fatalf("validate refused the orchestrator-shaped manifest: %v", err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := writeYBSnapshotManifest(context.Background(), path, manifest); err != nil {
		t.Fatalf("writeYBSnapshotManifest: %v", err)
	}
}

// TestFetchYBSnapshotManifestRefusesAForeignRunID fetches manifests through the
// real object-store client and proves a manifest is refused when it declares a
// run other than the prefix it was fetched from. Every downstream path keys off
// the manifest's own run id rather than the prefix, so a manifest planted or
// copied under an older run's prefix would otherwise date the export from the
// run it names and walk the wrong prefix for the node archives.
func TestFetchYBSnapshotManifestRefusesAForeignRunID(t *testing.T) {
	ctx := context.Background()
	const storedUnder = "20260829T100000Z"
	const declares = "20260830T100000Z"

	foreign := newYBSnapshotManifest(declares, "snap-1", "tack", []string{"yb1"})
	s3Client, cfg := newFakeBackupObjectStore(t, "tack-backups",
		fakeYBExportRunObjects(t, storedUnder, foreign))
	_, err := fetchYBSnapshotManifest(ctx, s3Client, cfg.BackupS3BucketMain, storedUnder)
	if err == nil {
		t.Fatalf("a manifest declaring run %s under the prefix of run %s must be refused", declares, storedUnder)
	}
	if !strings.Contains(err.Error(), "is not the run") {
		t.Fatalf("the error must name the disagreement, got: %v", err)
	}

	own := newYBSnapshotManifest(storedUnder, "snap-1", "tack", []string{"yb1"})
	ownClient, ownCfg := newFakeBackupObjectStore(t, "tack-backups",
		fakeYBExportRunObjects(t, storedUnder, own))
	got, err := fetchYBSnapshotManifest(ctx, ownClient, ownCfg.BackupS3BucketMain, storedUnder)
	if err != nil {
		t.Fatalf("a manifest under its own run prefix must fetch cleanly: %v", err)
	}
	if got.RunID != storedUnder {
		t.Fatalf("fetched run_id = %q, want %q", got.RunID, storedUnder)
	}
}

// TestYBNodeArchiveKey pins the full object key the archive command uploads to
// and the restore gate checks, so the two sides can never drift apart.
func TestYBNodeArchiveKey(t *testing.T) {
	node := ybSnapshotManifestNode{Name: "yb1", Prefix: "nodes/yb1/"}
	got := ybNodeArchiveKey("20260826T030000Z", node)
	want := "yugabyte-snapshot/20260826T030000Z/nodes/yb1/tablets.tar.gz"
	if got != want {
		t.Fatalf("archive key mismatch:\n got=%s\nwant=%s", got, want)
	}
}

// TestMissingYBNodeArchives exercises the completeness rule the restore drill
// gates on: only nodes whose archive object is absent are reported, in
// manifest order.
func TestMissingYBNodeArchives(t *testing.T) {
	manifest := newYBSnapshotManifest("run-1", "snap-1", "tack", []string{"yb1", "yb2", "yb3"})
	present := map[string]bool{
		ybNodeArchiveKey("run-1", ybSnapshotManifestNode{Name: "yb1", Prefix: "nodes/yb1/"}): true,
		ybNodeArchiveKey("run-1", ybSnapshotManifestNode{Name: "yb3", Prefix: "nodes/yb3/"}): true,
	}
	missing, err := missingYBNodeArchives(manifest, func(key string) (bool, error) {
		return present[key], nil
	})
	if err != nil {
		t.Fatalf("missingYBNodeArchives: %v", err)
	}
	if !reflect.DeepEqual(missing, []string{"yb2"}) {
		t.Fatalf("missing = %v, want [yb2]", missing)
	}

	allPresent, err := missingYBNodeArchives(manifest, func(string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("missingYBNodeArchives all present: %v", err)
	}
	if len(allPresent) != 0 {
		t.Fatalf("missing = %v, want none", allPresent)
	}
}

// TestParseYBTabletServers parses the column layout yb-admin
// list_all_tablet_servers actually prints (fixture captured live from
// 2024.2.8.0 on 2026-08-28): a header row, then one row per server with the
// undashed 32-hex UUID first, the advertised RPC host:port second, and the
// Status fourth. A DEAD server is excluded, because it can never archive its
// tablets and listing it would block the completeness gate forever.
// Duplicate hosts collapse and the result is sorted.
func TestParseYBTabletServers(t *testing.T) {
	out := "Tablet Server UUID               RPC Host/Port Heartbeat delay Status   Reads/s  Writes/s Uptime   SST total size  SST uncomp size SST #files      Memory   Broadcast Host/Port \n" +
		"4f5e4f2de0294c44bc30c15d1e4ce337 yb2:9100 0.77s           ALIVE    0.00     0.20     1514     1.03 GB         9.58 GB         62              189.01 MB yb2:9100\n" +
		"44f39a5171aa432d9d1ed77a234da0d7 yb3:9100 60.54s          DEAD     0.00     0.00     0        1.03 GB         9.58 GB         59              201.66 MB yb3:9100\n" +
		"fb84db84104f43ffb3e5c33d409242ef yb1:9100 0.74s           ALIVE    0.00     0.00     1522     1.03 GB         9.58 GB         63              203.60 MB yb1:9100\n" +
		"not-a-uuid                       bogus:9100 0.10s         ALIVE    0.00     0.00     1        0 B             0 B             0               0 B      bogus:9100\n"
	got := parseYBTabletServers(out)
	if !reflect.DeepEqual(got, []string{"yb1", "yb2"}) {
		t.Fatalf("tablet servers = %v, want [yb1 yb2] with the DEAD yb3 excluded", got)
	}
}

// TestParseYBTabletServersIPv6Literal keeps the parser correct if a server
// ever registers a bracketed IPv6 literal instead of a name.
func TestParseYBTabletServersIPv6Literal(t *testing.T) {
	out := "4f5e4f2de0294c44bc30c15d1e4ce337 [3d06:bad:b01::10]:9100 0.10s ALIVE\n"
	got := parseYBTabletServers(out)
	if !reflect.DeepEqual(got, []string{"3d06:bad:b01::10"}) {
		t.Fatalf("tablet servers = %v, want the unbracketed IPv6 host", got)
	}
}

// TestYBFirstMasterHost covers the three master_addresses shapes the schema
// one-shot must resolve: single name, quorum list, and bracketed IPv6 literal.
func TestYBFirstMasterHost(t *testing.T) {
	tests := []struct {
		masters string
		want    string
	}{
		{masters: "yugabyte:7100", want: "yugabyte"},
		{masters: "yb1:7100,yb2:7100,yb3:7100", want: "yb1"},
		{masters: "[3d06:bad:b01::10]:7100,yb2:7100", want: "3d06:bad:b01::10"},
	}
	for _, test := range tests {
		if got := ybFirstMasterHost(test.masters); got != test.want {
			t.Errorf("ybFirstMasterHost(%q) = %q, want %q", test.masters, got, test.want)
		}
	}
}
