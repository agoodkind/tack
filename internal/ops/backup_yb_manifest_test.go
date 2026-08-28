package ops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
	manifest := newYBSnapshotManifest("run-1", "snap-1", "tack", []string{"yb1", "yb2"})
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

// TestParseYBTabletServers parses the real column layout yb-admin
// list_all_tablet_servers prints: a header row, then one row per server with
// the UUID first, the advertised RPC host:port second, and the Status fourth.
// A DEAD server is excluded, because it can never archive its tablets and
// listing it would block the completeness gate forever. Duplicate hosts
// collapse and the result is sorted.
func TestParseYBTabletServers(t *testing.T) {
	out := "Tablet Server UUID                       RPC Host/Port    Heartbeat delay Status   Reads/s  Writes/s Uptime\n" +
		"1a2b3c4d-1111-2222-3333-444455556666         yb2:9100         0.32s      ALIVE    0.00     0.00     12345\n" +
		"9f8e7d6c-aaaa-bbbb-cccc-ddddeeeeffff         yb1:9100         0.10s      ALIVE    0.00     0.00     12345\n" +
		"deadbeef-aaaa-bbbb-cccc-ddddeeeeffff         yb3:9100         60.00s     DEAD     0.00     0.00     0\n" +
		"not-a-uuid                                   bogus:9100       0.10s      ALIVE    0.00     0.00     1\n"
	got := parseYBTabletServers(out)
	if !reflect.DeepEqual(got, []string{"yb1", "yb2"}) {
		t.Fatalf("tablet servers = %v, want [yb1 yb2] with the DEAD yb3 excluded", got)
	}
}

// TestParseYBTabletServersIPv6Literal keeps the parser correct if a server
// ever registers a bracketed IPv6 literal instead of a name.
func TestParseYBTabletServersIPv6Literal(t *testing.T) {
	out := "1a2b3c4d-1111-2222-3333-444455556666 [3d06:bad:b01::10]:9100 0.10s ALIVE\n"
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
