package ops

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"goodkind.io/tack/internal/config"
)

// TestYBSnapshotUploadArtifactsManifestLast locks the upload order the export
// relies on: the manifest is the completeness gate, so it must be the last
// artifact in the slice and land only after everything it vouches for.
func TestYBSnapshotUploadArtifactsManifestLast(t *testing.T) {
	files := ybSnapshotUploadArtifacts("/stage", "/stage/schema.sql", "/stage/manifest.json")
	if len(files) != 3 {
		t.Fatalf("artifact count = %d, want 3", len(files))
	}
	last := files[len(files)-1]
	if last.name != ybSnapshotManifestObject {
		t.Fatalf("last artifact = %q, want the manifest %q", last.name, ybSnapshotManifestObject)
	}
	if files[0].name != ybSnapshotMetadataObject || files[1].name != "schema.sql" {
		t.Fatalf("artifact order = %v, want metadata then schema then manifest", files)
	}
}

// TestUploadYBSnapshotArtifactsUploadsInOrder proves the uploader preserves
// slice order, so the manifest-last contract survives the actual upload loop.
func TestUploadYBSnapshotArtifactsUploadsInOrder(t *testing.T) {
	var uploaded []string
	putYBSnapshotObject = func(_ context.Context, _ *s3.Client, _, key, _ string) error {
		uploaded = append(uploaded, key)
		return nil
	}
	t.Cleanup(func() { putYBSnapshotObject = putObjectFromFile })

	cfg := &config.Config{BackupS3BucketMain: "backups"}
	files := ybSnapshotUploadArtifacts("/stage", "/stage/schema.sql", "/stage/manifest.json")
	if err := uploadYBSnapshotArtifacts(context.Background(), cfg, "run-1", files); err != nil {
		t.Fatalf("uploadYBSnapshotArtifacts: %v", err)
	}
	want := []string{
		"yugabyte-snapshot/run-1/metadata.snapshot",
		"yugabyte-snapshot/run-1/schema.sql",
		"yugabyte-snapshot/run-1/manifest.json",
	}
	if !reflect.DeepEqual(uploaded, want) {
		t.Fatalf("upload order:\n got=%v\nwant=%v", uploaded, want)
	}
}

// TestUploadYBSnapshotArtifactsStopsBeforeManifest proves a failed payload
// upload prevents the manifest from ever landing, so a partially uploaded run
// can never pass the completeness gate.
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
	files := ybSnapshotUploadArtifacts("/stage", "/stage/schema.sql", "/stage/manifest.json")
	err := uploadYBSnapshotArtifacts(context.Background(), cfg, "run-1", files)
	if err == nil {
		t.Fatal("uploadYBSnapshotArtifacts must fail when a payload upload fails")
	}
	for _, key := range uploaded {
		if key == "yugabyte-snapshot/run-1/manifest.json" {
			t.Fatal("manifest uploaded even though a gated payload failed")
		}
	}
}

// TestParseYBClusterSnapshots parses list_snapshots output into id and state,
// ignoring the header, non-UUID rows, and the restoration section whose ids
// are also UUIDs but are not snapshots.
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

// TestUnmarshalYBScheduleSnapshotIDs decodes the documented
// list_snapshot_schedules JSON into the set of schedule-owned snapshot ids,
// and rejects unparseable output so the orphan pass is skipped rather than
// run against an unknown schedule state.
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

// TestReconcileYBOrphanSnapshots exercises the orphan verdicts: snapshots a
// walked manifest references are ignored, schedule-owned snapshots and
// non-deletable states are kept with a reason, and the rest are deleted as
// leftovers of exports that died before their manifest upload.
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

// ybCleanupManifestFixture is a satisfied one-node manifest for runID whose
// snapshot id is snapshotID, for cleanupYBExportRuns tests.
func ybCleanupManifestFixture(runID, snapshotID string) ybSnapshotManifest {
	return ybSnapshotManifest{
		RunID:      runID,
		SnapshotID: snapshotID,
		Database:   "tack",
		Nodes:      []ybSnapshotManifestNode{{Name: "yb1", Prefix: "nodes/yb1/"}},
	}
}

// TestCleanupYBExportRunsManifestErrorSkipsOrphanPass proves a non-NotFound
// manifest fetch failure on one run forbids the orphan pass, because that
// run's snapshot id never reached the reference set, while the satisfied-run
// deletion for the other walked runs still happens. A NotFound manifest on a
// third run stays a clean skip and does not forbid anything on its own.
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

// TestCleanupYBExportRunsMissingManifestAllowsOrphanPass proves a NotFound
// manifest counts as cleanly resolved: a run that never uploaded its manifest
// is exactly what the orphan pass exists to reap, so it must not forbid it.
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

// TestCleanupYBExportRunsNoRunsSkipsOrphanPass proves an empty run listing
// forbids the orphan pass: with no manifests to reference, every non-schedule
// cluster snapshot would look like an orphan and be deleted.
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
