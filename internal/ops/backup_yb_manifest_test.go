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

// ybTestArtifactNames derives the artifacts declared by an export manifest from
// the production upload list. Tests do not maintain a separate artifact list.
func ybTestArtifactNames() []string {
	return ybSnapshotGatedArtifactNames(
		ybSnapshotUploadArtifacts(
			"/stage",
			"/stage/schema.sql",
			"/stage/roles.sql",
			"/stage/manifest.json",
		),
	)
}

// TestNewYBSnapshotManifestAssignsNodePrefixes checks the manifest's
// completeness information.
//
// The test checks these fields:
//   - Each tablet server has one entry, sorted by name.
//   - Each entry has the run-relative prefix its archive command must fill.
//   - The run-root artifacts follow upload order.
func TestNewYBSnapshotManifestAssignsNodePrefixes(t *testing.T) {
	nowFunc = func() time.Time { return time.Date(2026, 8, 26, 3, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { nowFunc = time.Now })

	manifest := newYBSnapshotManifest(
		"20260826T030000Z",
		"snap-id",
		"tack",
		[]string{"yb2", "yb1", "yb3"},
		ybTestArtifactNames(),
	)

	wantArtifacts := []string{"metadata.snapshot", "schema.sql", "roles.sql"}
	if !reflect.DeepEqual(manifest.Artifacts, wantArtifacts) {
		t.Fatalf("artifacts mismatch:\n got=%v\nwant=%v", manifest.Artifacts, wantArtifacts)
	}

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

// TestYBSnapshotManifestFileRoundTrip writes a manifest with the production
// writer and decodes it with the JSON tags that the fetch path uses. The
// equality assertion checks field compatibility for this round trip.
func TestYBSnapshotManifestFileRoundTrip(t *testing.T) {
	manifest := newYBSnapshotManifest(
		"20260826T030000Z",
		"snap-1",
		"tack",
		[]string{"yb1", "yb2"},
		ybTestArtifactNames(),
	)
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

// TestYBSnapshotManifestValidateRejectsTraversalRunID checks that validation
// and the production writer reject invalid run IDs, including traversal
// segments. Fetches validate each manifest. Staging paths use the run ID with
// [filepath.Join], and cleanup removes those directories recursively. Run IDs
// with traversal segments must fail validation before readers or writers use
// them.
func TestYBSnapshotManifestValidateRejectsTraversalRunID(t *testing.T) {
	for _, runID := range []string{"../../../etc", "20260826T030000Z/../..", "run-1", ""} {
		manifest := newYBSnapshotManifest(
			runID,
			"snap-1",
			"tack",
			[]string{"yb1"},
			ybTestArtifactNames(),
		)
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

// TestYBSnapshotManifestValidateRejectsBadNodeNames checks that validation
// rejects path separators, traversal segments, and shell metacharacters in node
// names. Node names determine staged archive filenames and extraction paths
// inside the container. Validation also rejects a prefix that differs from the
// prefix derived from the node name. Uploads use that prefix in the object key.
func TestYBSnapshotManifestValidateRejectsBadNodeNames(t *testing.T) {
	badNames := []string{
		"../yb1",
		"a..b",
		"nodes/yb1",
		`yb\1`,
		"yb1;reboot",
		"yb$(reboot)",
		"yb1 yb2",
		"-yb1",
		"",
	}
	for _, name := range badNames {
		manifest := ybSnapshotManifest{
			RunID:      "20260826T030000Z",
			SnapshotID: "snap-1",
			Database:   "tack",
			CreatedAt:  "2026-08-26T03:00:00Z",
			Artifacts:  ybTestArtifactNames(),
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
		Artifacts:  ybTestArtifactNames(),
		Nodes:      []ybSnapshotManifestNode{{Name: "yb1", Prefix: "nodes/yb2/"}},
	}
	if err := mismatched.validate(); err == nil {
		t.Error("validate accepted a prefix that disagrees with its node name")
	}
}

// TestYBSnapshotManifestValidateAcceptsOrchestratorShape checks validation and
// the production writer with the orchestrator's manifest format. The run ID
// uses the run-key timestamp format. Nodes accept DNS names and unbracketed
// IPv6 literals. hostFromHostPort removes the brackets from a bracketed
// host:port value.
func TestYBSnapshotManifestValidateAcceptsOrchestratorShape(t *testing.T) {
	manifest := newYBSnapshotManifest(
		"20260826T030000Z",
		"snap-1",
		"tack",
		[]string{"yb1", "yb2", "yb3", "3d06:bad:b01::10"},
		ybTestArtifactNames(),
	)
	if err := manifest.validate(); err != nil {
		t.Fatalf("validate refused the orchestrator-shaped manifest: %v", err)
	}

	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := writeYBSnapshotManifest(context.Background(), path, manifest); err != nil {
		t.Fatalf("writeYBSnapshotManifest: %v", err)
	}
}

// TestFetchYBSnapshotManifestRefusesAForeignRunID uses the real object-store
// client to check that a fetched manifest declares the run in its storage
// prefix. Downstream paths use the manifest's run ID. A manifest copied under
// an older run's prefix must not change the export date or select another run's
// node archives.
func TestFetchYBSnapshotManifestRefusesAForeignRunID(t *testing.T) {
	ctx := context.Background()
	const storedUnder = "20260829T100000Z"
	const declares = "20260830T100000Z"

	foreign := newYBSnapshotManifest(
		declares,
		"snap-1",
		"tack",
		[]string{"yb1"},
		ybTestArtifactNames(),
	)
	foreignStore := newBackupTestStore(t, ybExportRunObjects(t, storedUnder, foreign))
	_, err := fetchYBSnapshotManifest(
		ctx,
		foreignStore.client,
		foreignStore.bucket.Bucket,
		storedUnder,
	)
	if err == nil {
		t.Fatalf(
			"a manifest declaring run %s under the prefix of run %s must be refused",
			declares,
			storedUnder,
		)
	}

	if !strings.Contains(err.Error(), "is not the run") {
		t.Fatalf("the error must name the disagreement, got: %v", err)
	}

	own := newYBSnapshotManifest(
		storedUnder,
		"snap-1",
		"tack",
		[]string{"yb1"},
		ybTestArtifactNames(),
	)
	ownStore := newBackupTestStore(t, ybExportRunObjects(t, storedUnder, own))
	got, err := fetchYBSnapshotManifest(ctx, ownStore.client, ownStore.bucket.Bucket, storedUnder)
	if err != nil {
		t.Fatalf("a manifest under its own run prefix must fetch cleanly: %v", err)
	}

	if got.RunID != storedUnder {
		t.Fatalf("fetched run_id = %q, want %q", got.RunID, storedUnder)
	}
}

// TestYBNodeArchiveKey checks the complete object key used by archive uploads
// and the restore completeness check.
func TestYBNodeArchiveKey(t *testing.T) {
	node := ybSnapshotManifestNode{Name: "yb1", Prefix: "nodes/yb1/"}
	got := ybNodeArchiveKey("20260826T030000Z", node)
	want := "yugabyte-snapshot/20260826T030000Z/nodes/yb1/tablets.tar.gz"
	if got != want {
		t.Fatalf("archive key mismatch:\n got=%s\nwant=%s", got, want)
	}
}

// TestMissingYBNodeArchives checks that the restore completeness check reports
// nodes with missing artifacts in manifest order. The fixture omits every yb2
// artifact and omits the yb3 inventory while retaining its archive. Restore
// requires the inventory to verify archive contents.
func TestMissingYBNodeArchives(t *testing.T) {
	manifest := newYBSnapshotManifest(
		"run-1",
		"snap-1",
		"tack",
		[]string{"yb1", "yb2", "yb3"},
		ybTestArtifactNames(),
	)
	yb1 := ybSnapshotManifestNode{Name: "yb1", Prefix: "nodes/yb1/"}
	yb3 := ybSnapshotManifestNode{Name: "yb3", Prefix: "nodes/yb3/"}
	present := map[string]bool{
		ybNodeArchiveKey("run-1", yb1):   true,
		ybNodeInventoryKey("run-1", yb1): true,
		ybNodeArchiveKey("run-1", yb3):   true,
	}
	missing, err := missingYBNodeArchives(
		manifest,
		func(key string) (bool, error) {
			return present[key], nil
		},
	)
	if err != nil {
		t.Fatalf("missingYBNodeArchives: %v", err)
	}

	if !reflect.DeepEqual(missing, []string{"yb2", "yb3"}) {
		t.Fatalf("missing = %v, want [yb2 yb3]", missing)
	}

	allPresent, err := missingYBNodeArchives(
		manifest,
		func(string) (bool, error) { return true, nil },
	)
	if err != nil {
		t.Fatalf("missingYBNodeArchives all present: %v", err)
	}

	if len(allPresent) != 0 {
		t.Fatalf("missing = %v, want none", allPresent)
	}
}

// TestMissingYBRunArtifacts checks that the completeness check reports missing
// declared run-root artifacts in manifest order. It does not report missing
// artifacts when every declared object exists.
func TestMissingYBRunArtifacts(t *testing.T) {
	manifest := newYBSnapshotManifest(
		"run-1",
		"snap-1",
		"tack",
		[]string{"yb1"},
		ybTestArtifactNames(),
	)
	present := map[string]bool{
		ybRunArtifactKey("run-1", ybSnapshotMetadataObject): true,
		ybRunArtifactKey("run-1", ybSnapshotSchemaObject):   true,
	}
	missing, err := missingYBRunArtifacts(
		manifest,
		func(key string) (bool, error) {
			return present[key], nil
		},
	)
	if err != nil {
		t.Fatalf("missingYBRunArtifacts: %v", err)
	}

	if !reflect.DeepEqual(missing, []string{ybSnapshotRolesObject}) {
		t.Fatalf("missing = %v, want [%s]", missing, ybSnapshotRolesObject)
	}

	allPresent, err := missingYBRunArtifacts(
		manifest,
		func(string) (bool, error) { return true, nil },
	)
	if err != nil {
		t.Fatalf("missingYBRunArtifacts all present: %v", err)
	}

	if len(allPresent) != 0 {
		t.Fatalf("missing = %v, want none", allPresent)
	}
}

// TestYBSnapshotManifestValidateRejectsBadArtifactNames checks that validation
// rejects path separators, traversal segments, and shell metacharacters in
// artifact names. The restore drill appends declared names to the staging
// directory and the run's object prefix. An untrusted manifest must not
// redirect either path.
func TestYBSnapshotManifestValidateRejectsBadArtifactNames(t *testing.T) {
	badNames := []string{
		"../schema.sql",
		"a..b",
		"nodes/schema.sql",
		`schema\.sql`,
		"schema.sql;reboot",
		"schema $(reboot)",
		"schema sql",
		"-schema.sql",
		"",
	}
	for _, name := range badNames {
		manifest := newYBSnapshotManifest(
			"20260826T030000Z",
			"snap-1",
			"tack",
			[]string{"yb1"},
			[]string{name},
		)
		if err := manifest.validate(); err == nil {
			t.Errorf("validate accepted artifact name %q", name)
		}
	}
}

// TestYBNodeArtifactKeysGateEveryPublishedObject compares completeness keys
// with the archive command's upload declaration. Declaring an added artifact
// must include it in both uploads and completeness checks.
func TestYBNodeArtifactKeysGateEveryPublishedObject(t *testing.T) {
	node := ybSnapshotManifestNode{Name: "yb1", Prefix: "nodes/yb1/"}
	keys := ybNodeArtifactKeys("20260826T030000Z", node)
	want := []string{
		"yugabyte-snapshot/20260826T030000Z/nodes/yb1/tablets.inventory",
		"yugabyte-snapshot/20260826T030000Z/nodes/yb1/tablets.tar.gz",
	}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("node artifact keys:\n got=%v\nwant=%v", keys, want)
	}

	if keys[len(keys)-1] != ybNodeArchiveKey("20260826T030000Z", node) {
		t.Fatalf("the archive must be the last artifact, got %v", keys)
	}

	uploaded := ybNodeUploadArtifacts("/stage")
	for i, file := range uploaded {
		if !strings.HasSuffix(keys[i], "/"+file.name) || file.path != "/stage/"+file.name {
			t.Fatalf("upload %d is %+v, which does not match gated key %s", i, file, keys[i])
		}
	}
}

// TestParseYBTabletServers checks the yb-admin list_all_tablet_servers layout.
// The fixture records output from version 2024.2.8.0 on 2026-08-28, with a
// header followed by one row per server.
//
// The server rows use these columns:
//   - The first column contains an undashed 32-digit hexadecimal UUID.
//   - The second column contains the advertised RPC host:port value.
//   - The fourth column contains Status.
//
// The parser excludes DEAD servers, which cannot archive their tablets and
// would prevent export completion. It removes duplicate hosts and sorts the
// result.
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

// TestParseYBTabletServersIPv6Literal checks parsing when a server registers a
// bracketed IPv6 literal instead of a DNS name.
func TestParseYBTabletServersIPv6Literal(t *testing.T) {
	out := "4f5e4f2de0294c44bc30c15d1e4ce337 [3d06:bad:b01::10]:9100 0.10s ALIVE\n"
	got := parseYBTabletServers(out)
	if !reflect.DeepEqual(got, []string{"3d06:bad:b01::10"}) {
		t.Fatalf("tablet servers = %v, want the unbracketed IPv6 host", got)
	}
}

// TestUndeclaredYBRequiredArtifacts checks the manifest declaration without
// consulting object storage. It reports required restore artifacts that the
// manifest omits and accepts the production export declaration.
//
// Restore requires all three artifacts even when object storage contains them.
// An omitted declaration differs from a missing object: presence checks examine
// only declared artifacts and cannot detect an omitted declaration.
func TestUndeclaredYBRequiredArtifacts(t *testing.T) {
	tests := []struct {
		name      string
		artifacts []string
		want      []string
	}{
		{
			name:      "an export written before the run carried its roles file",
			artifacts: nil,
			want: []string{
				ybSnapshotMetadataObject,
				ybSnapshotSchemaObject,
				ybSnapshotRolesObject,
			},
		},
		{
			name:      "a declaration truncated to one artifact",
			artifacts: []string{ybSnapshotMetadataObject},
			want:      []string{ybSnapshotSchemaObject, ybSnapshotRolesObject},
		},
		{
			name:      "the roles file left out of an otherwise full declaration",
			artifacts: []string{ybSnapshotMetadataObject, ybSnapshotSchemaObject},
			want:      []string{ybSnapshotRolesObject},
		},
		{
			name:      "what a real export declares",
			artifacts: ybTestArtifactNames(),
			want:      nil,
		},
	}
	for _, test := range tests {
		t.Run(
			test.name,
			func(t *testing.T) {
				manifest := newYBSnapshotManifest(
					"20260826T030000Z",
					"snap-1",
					"tack",
					[]string{"yb1"},
					test.artifacts,
				)
				got := undeclaredYBRequiredArtifacts(manifest)
				if !reflect.DeepEqual(got, test.want) {
					t.Fatalf("undeclared:\n got=%v\nwant=%v", got, test.want)
				}
			},
		)
	}
}

// TestWriteYBSnapshotManifestRefusesATruncatedDeclaration checks that the
// production writer rejects an export manifest that omits an artifact restore
// requires. The check runs during export, while failure can still stop
// publication. The writer does not create a manifest file after rejection.
func TestWriteYBSnapshotManifestRefusesATruncatedDeclaration(t *testing.T) {
	manifest := newYBSnapshotManifest(
		"20260826T030000Z",
		"snap-1",
		"tack",
		[]string{"yb1"},
		[]string{ybSnapshotMetadataObject, ybSnapshotSchemaObject},
	)
	path := filepath.Join(t.TempDir(), "manifest.json")
	err := writeYBSnapshotManifest(context.Background(), path, manifest)
	if err == nil {
		t.Fatal("a manifest omitting the roles file must not be written")
	}

	if !strings.Contains(err.Error(), ybSnapshotRolesObject) {
		t.Fatalf("the error must name the undeclared artifact, got: %v", err)
	}

	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("manifest file written despite a truncated declaration")
	}
}
