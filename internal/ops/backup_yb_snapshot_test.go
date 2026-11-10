package ops

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"goodkind.io/tack/internal/config"
)

// TestYBSnapshotUploadArtifactsManifestLast checks that the export declaration
// places the manifest after every artifact it declares. The roles file provides
// the identities required by the schema's grants during restore.
func TestYBSnapshotUploadArtifactsManifestLast(t *testing.T) {
	files := ybSnapshotUploadArtifacts("/stage", "/stage/schema.sql", "/stage/roles.sql", "/stage/manifest.json")
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.name)
	}
	want := []string{
		ybSnapshotMetadataObject, ybSnapshotSchemaObject,
		ybSnapshotRolesObject, ybSnapshotManifestObject,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("artifact order:\n got=%v\nwant=%v", got, want)
	}
}

// TestYBSnapshotGatedArtifactNamesExcludeTheManifest checks that the manifest
// declares every uploaded artifact except itself. Readers discover runs through
// manifest presence; declaring the manifest would not add a completeness check.
// The declaration derives from the upload list.
func TestYBSnapshotGatedArtifactNamesExcludeTheManifest(t *testing.T) {
	files := ybSnapshotUploadArtifacts("/stage", "/stage/schema.sql", "/stage/roles.sql", "/stage/manifest.json")
	got := ybSnapshotGatedArtifactNames(files)
	want := []string{ybSnapshotMetadataObject, ybSnapshotSchemaObject, ybSnapshotRolesObject}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gated artifacts:\n got=%v\nwant=%v", got, want)
	}
}

// TestYBSchemaDumpArgsDescribePrivileges checks that the schema dump requests
// database access-control statements.
//
// TACK-474 recorded a dump that used --no-privileges and omitted GRANT and
// REVOKE statements. Restore recreated tables and row-level security policies
// without the required privileges. Application roles, including the
// audit_reader used for ledger reads, could not read the restored ledger.
// Recovery did not restore those grants elsewhere because it did not rerun the
// migration that granted them.
func TestYBSchemaDumpArgsDescribePrivileges(t *testing.T) {
	args := ybSchemaDumpArgs(&config.Config{YugabyteDB: "tack"})
	for _, banned := range []string{"--no-privileges", "-x", "--dump-role-checks"} {
		if slices.Contains(args, banned) {
			t.Fatalf("schema dump passes %s, which drops or conditions the grants: %v", banned, args)
		}
	}
	for _, required := range []string{"--schema-only", "--include-yb-metadata", "--no-owner"} {
		if !slices.Contains(args, required) {
			t.Fatalf("schema dump is missing %s: %v", required, args)
		}
	}
	if !slices.Contains(args, "-d") || !slices.Contains(args, "tack") {
		t.Fatalf("schema dump does not name the database: %v", args)
	}
}

// TestYBRolesDumpArgsCarryIdentitiesNotCredentials checks that the roles dump
// requests cluster roles without passwords. The schema dump covers one database
// and cannot define cluster roles. Restore must create those roles before
// applying the schema's grants.
func TestYBRolesDumpArgsCarryIdentitiesNotCredentials(t *testing.T) {
	args := ybRolesDumpArgs(&config.Config{YugabyteDB: "tack"})
	for _, required := range []string{"--roles-only", "--no-role-passwords"} {
		if !slices.Contains(args, required) {
			t.Fatalf("roles dump is missing %s: %v", required, args)
		}
	}
	if !slices.Contains(args, ybDumpOutDir+"/"+ybSnapshotRolesObject) {
		t.Fatalf("roles dump does not write the roles artifact: %v", args)
	}
}

// TestUploadYBSnapshotArtifactsUploadsInOrder checks that the upload loop
// preserves slice order, including the manifest's final position.
func TestUploadYBSnapshotArtifactsUploadsInOrder(t *testing.T) {
	var uploaded []string
	putYBSnapshotObject = func(_ context.Context, _ *s3.Client, _, key, _ string) error {
		uploaded = append(uploaded, key)
		return nil
	}
	t.Cleanup(func() { putYBSnapshotObject = putObjectFromFile })

	cfg := &config.Config{BackupS3BucketMain: "backups"}
	files := ybSnapshotUploadArtifacts("/stage", "/stage/schema.sql", "/stage/roles.sql", "/stage/manifest.json")
	if err := uploadYBSnapshotArtifacts(context.Background(), cfg, ybSnapshotKeyPrefix("run-1"), files); err != nil {
		t.Fatalf("uploadYBSnapshotArtifacts: %v", err)
	}
	want := []string{
		"yugabyte-snapshot/run-1/metadata.snapshot",
		"yugabyte-snapshot/run-1/schema.sql",
		"yugabyte-snapshot/run-1/roles.sql",
		"yugabyte-snapshot/run-1/manifest.json",
	}
	if !reflect.DeepEqual(uploaded, want) {
		t.Fatalf("upload order:\n got=%v\nwant=%v", uploaded, want)
	}
}

// TestYBNodeArtifactsUploadTheArchiveLast checks that a node uploads its
// inventory before its archive. Completeness checks and the archive command's
// repeat-run check require both objects. Uploading inventory first prevents
// publication of an archive before its inventory.
func TestYBNodeArtifactsUploadTheArchiveLast(t *testing.T) {
	var uploaded []string
	putYBSnapshotObject = func(_ context.Context, _ *s3.Client, _, key, _ string) error {
		uploaded = append(uploaded, key)
		return nil
	}
	t.Cleanup(func() { putYBSnapshotObject = putObjectFromFile })

	cfg := &config.Config{BackupS3BucketMain: "backups"}
	node := ybSnapshotManifestNode{Name: "yb1", Prefix: "nodes/yb1/"}
	prefix := ybNodeKeyPrefix("run-1", node)
	if err := uploadYBSnapshotArtifacts(context.Background(), cfg, prefix, ybNodeUploadArtifacts("/stage")); err != nil {
		t.Fatalf("uploadYBSnapshotArtifacts: %v", err)
	}
	want := []string{
		"yugabyte-snapshot/run-1/nodes/yb1/tablets.inventory",
		"yugabyte-snapshot/run-1/nodes/yb1/tablets.tar.gz",
	}
	if !reflect.DeepEqual(uploaded, want) {
		t.Fatalf("node upload order:\n got=%v\nwant=%v", uploaded, want)
	}
}

// TestUploadYBSnapshotArtifactsStopsBeforeManifest checks that a failed payload
// upload stops the uploader before it publishes the manifest. A partial upload
// must not satisfy the restore completeness check.
func TestUploadYBSnapshotArtifactsStopsBeforeManifest(t *testing.T) {
	var uploaded []string
	putYBSnapshotObject = func(_ context.Context, _ *s3.Client, _, key, _ string) error {
		if key == "yugabyte-snapshot/run-1/schema.sql" {
			return fmt.Errorf("schema upload failed")
		}
		uploaded = append(uploaded, key)
		return nil
	}
	t.Cleanup(func() { putYBSnapshotObject = putObjectFromFile })

	cfg := &config.Config{BackupS3BucketMain: "backups"}
	files := ybSnapshotUploadArtifacts("/stage", "/stage/schema.sql", "/stage/roles.sql", "/stage/manifest.json")
	err := uploadYBSnapshotArtifacts(context.Background(), cfg, ybSnapshotKeyPrefix("run-1"), files)
	if err == nil {
		t.Fatal("uploadYBSnapshotArtifacts must fail when a payload upload fails")
	}
	for _, key := range uploaded {
		if key == "yugabyte-snapshot/run-1/manifest.json" {
			t.Fatal("manifest uploaded even though a gated payload failed")
		}
	}
}

// TestParseYBClusterSnapshots checks snapshot IDs and states from
// list_snapshots output. The parser ignores the header, rows without UUIDs, and
// the restoration section, which also uses UUIDs.
func TestParseYBClusterSnapshots(t *testing.T) {
	out := "Snapshot UUID                            State      Creation Time\n" +
		"11111111-1111-2222-3333-444455556666     COMPLETE   2026-08-26 03:00:00\n" +
		"22222222-1111-2222-3333-444455556666     DELETING   2026-08-25 03:00:00\n" +
		"Restoration UUID                         State\n" +
		"33333333-1111-2222-3333-444455556666     RESTORED\n"
	got := parseYBClusterSnapshots(out)
	want := map[string]ybSnapshotStatus{
		"11111111-1111-2222-3333-444455556666": ybSnapStatusComplete,
		"22222222-1111-2222-3333-444455556666": ybSnapshotStatus("DELETING"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshots:\n got=%v\nwant=%v", got, want)
	}
}

// TestUnmarshalYBScheduleSnapshotIDs checks the schedule-owned snapshot IDs
// decoded from list_snapshot_schedules JSON and rejects malformed output.
// Orphan cleanup skips deletion when it cannot determine schedule ownership.
func TestUnmarshalYBScheduleSnapshotIDs(t *testing.T) {
	out := `{"schedules":[` +
		`{"id":"6eaaa4fb-397f-41e2-a8fe-a93e0c9f5256","snapshots":[{"id":"aaaa1111-1111-2222-3333-444455556666"}]},` +
		`{"id":"7eaaa4fb-397f-41e2-a8fe-a93e0c9f5256","snapshots":[{"id":"bbbb1111-1111-2222-3333-444455556666"},{"id":"cccc1111-1111-2222-3333-444455556666"}]}]}`
	got, err := unmarshalYBScheduleSnapshotIDs(context.Background(), out)
	if err != nil {
		t.Fatalf("unmarshalYBScheduleSnapshotIDs: %v", err)
	}
	want := map[string]bool{
		"aaaa1111-1111-2222-3333-444455556666": true,
		"bbbb1111-1111-2222-3333-444455556666": true,
		"cccc1111-1111-2222-3333-444455556666": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("schedule snapshots:\n got=%v\nwant=%v", got, want)
	}

	if _, err := unmarshalYBScheduleSnapshotIDs(context.Background(), "not json"); err == nil {
		t.Fatal("unparseable schedule output must error, not return an empty set")
	}
}

// TestReconcileYBOrphanSnapshots checks deletion decisions for cluster
// snapshots. The function omits referenced snapshots, retains schedule-owned
// snapshots and states that do not permit deletion, with reasons, and marks
// other snapshots for deletion. Those unreferenced snapshots can remain after
// an export fails before manifest upload.
func TestReconcileYBOrphanSnapshots(t *testing.T) {
	states := map[string]ybSnapshotStatus{
		"referenced-1": ybSnapStatusComplete,
		"orphan-1":     ybSnapStatusComplete,
		"orphan-2":     ybSnapStatusFailed,
		"creating-1":   ybSnapStatusCreating,
		"schedule-1":   ybSnapStatusComplete,
	}
	referenced := map[string]bool{"referenced-1": true}
	scheduleOwned := map[string]bool{"schedule-1": true}

	got := reconcileYBOrphanSnapshots(states, referenced, scheduleOwned)
	want := []ybOrphanSnapshotDisposition{
		{id: "creating-1", delete: false, reason: "state CREATING is not deletable"},
		{id: "orphan-1", delete: true, reason: "no export run manifest references it"},
		{id: "orphan-2", delete: true, reason: "no export run manifest references it"},
		{id: "schedule-1", delete: false, reason: "owned by a snapshot schedule"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dispositions:\n got=%+v\nwant=%+v", got, want)
	}
}

// ybCleanupManifestFixture returns a complete one-node manifest for
// cleanupYBExportRuns tests, using runID and snapshotID.
func ybCleanupManifestFixture(runID, snapshotID string) ybSnapshotManifest {
	return ybSnapshotManifest{
		RunID:      runID,
		SnapshotID: snapshotID,
		Database:   "tack",
		Nodes:      []ybSnapshotManifestNode{{Name: "yb1", Prefix: "nodes/yb1/"}},
	}
}

// TestCleanupYBExportRunsManifestErrorSkipsOrphanPass checks that a manifest
// fetch error other than NotFound prevents orphan cleanup. The unresolved run
// cannot contribute its snapshot ID to the reference set. Cleanup still deletes
// a different run's fully archived snapshot. A third run's NotFound result does
// not independently prevent orphan cleanup.
func TestCleanupYBExportRunsManifestErrorSkipsOrphanPass(t *testing.T) {
	states := map[string]ybSnapshotStatus{
		"snap-3":   ybSnapStatusComplete,
		"orphan-1": ybSnapStatusComplete,
	}
	fetch := func(runID string) (ybSnapshotManifest, error) {
		switch runID {
		case "run-1":
			return ybSnapshotManifest{}, fmt.Errorf("get manifest: %w", &s3types.NotFound{})
		case "run-2":
			return ybSnapshotManifest{}, fmt.Errorf("get manifest: 503 service unavailable")
		default:
			return ybCleanupManifestFixture(runID, "snap-3"), nil
		}
	}
	exists := func(string) (bool, error) { return true, nil }
	var deleted []string
	deleteSnapshot := func(snapshotID string) error {
		deleted = append(deleted, snapshotID)
		return nil
	}

	referenced, orphanPassOK := cleanupYBExportRuns(context.Background(),
		[]string{"run-1", "run-2", "run-3"}, states, fetch, exists, deleteSnapshot)

	if orphanPassOK {
		t.Fatal("orphan pass allowed even though run-2's manifest fetch failed")
	}
	if !reflect.DeepEqual(deleted, []string{"snap-3"}) {
		t.Fatalf("deleted = %v, want only the satisfied run's snapshot [snap-3]", deleted)
	}
	if !referenced["snap-3"] {
		t.Fatal("satisfied run's snapshot missing from the reference set")
	}
}

// TestCleanupYBExportRunsMissingManifestAllowsOrphanPass checks that a NotFound
// manifest counts as resolved. Orphan cleanup must allow for exports that
// failed before uploading a manifest.
func TestCleanupYBExportRunsMissingManifestAllowsOrphanPass(t *testing.T) {
	states := map[string]ybSnapshotStatus{"snap-2": ybSnapStatusComplete}
	fetch := func(runID string) (ybSnapshotManifest, error) {
		if runID == "run-1" {
			return ybSnapshotManifest{}, fmt.Errorf("get manifest: %w", &s3types.NotFound{})
		}
		return ybCleanupManifestFixture(runID, "snap-2"), nil
	}
	exists := func(string) (bool, error) { return true, nil }
	var deleted []string
	deleteSnapshot := func(snapshotID string) error {
		deleted = append(deleted, snapshotID)
		return nil
	}

	referenced, orphanPassOK := cleanupYBExportRuns(context.Background(),
		[]string{"run-1", "run-2"}, states, fetch, exists, deleteSnapshot)

	if !orphanPassOK {
		t.Fatal("orphan pass forbidden even though every manifest resolved cleanly")
	}
	if !reflect.DeepEqual(deleted, []string{"snap-2"}) {
		t.Fatalf("deleted = %v, want [snap-2]", deleted)
	}
	if !referenced["snap-2"] {
		t.Fatal("satisfied run's snapshot missing from the reference set")
	}
}

// TestCleanupYBExportRunsNoRunsSkipsOrphanPass checks that an empty run listing
// prevents orphan cleanup. Without manifest references, cleanup cannot
// distinguish an export's snapshot from an orphan outside the snapshot
// schedule.
func TestCleanupYBExportRunsNoRunsSkipsOrphanPass(t *testing.T) {
	fetch := func(string) (ybSnapshotManifest, error) {
		t.Fatal("fetch must not be called with no run prefixes")
		return ybSnapshotManifest{}, nil
	}
	exists := func(string) (bool, error) { return true, nil }
	deleteSnapshot := func(snapshotID string) error {
		t.Fatalf("delete_snapshot issued for %s with no run prefixes", snapshotID)
		return nil
	}

	referenced, orphanPassOK := cleanupYBExportRuns(context.Background(),
		nil, map[string]ybSnapshotStatus{"snap-1": ybSnapStatusComplete},
		fetch, exists, deleteSnapshot)

	if orphanPassOK {
		t.Fatal("orphan pass allowed with zero run prefixes")
	}
	if len(referenced) != 0 {
		t.Fatalf("referenced = %v, want empty", referenced)
	}
}
