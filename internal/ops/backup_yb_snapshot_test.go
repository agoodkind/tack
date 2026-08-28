package ops

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"

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
