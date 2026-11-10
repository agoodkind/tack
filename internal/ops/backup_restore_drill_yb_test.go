package ops

import (
	"fmt"
	"reflect"
	"testing"

	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// drillWalkFixture provides callbacks for checking older runs. It stores
// manifests by run ID, tracks present archive keys, and records every skip.
type drillWalkFixture struct {
	manifests map[string]ybSnapshotManifest
	present   map[string]bool
	skips     []string
}

func (f *drillWalkFixture) fetch(runID string) (ybSnapshotManifest, error) {
	manifest, ok := f.manifests[runID]
	if !ok {
		return ybSnapshotManifest{}, fmt.Errorf("get manifest %s: %w", runID, &s3types.NotFound{})
	}

	return manifest, nil
}

func (f *drillWalkFixture) exists(key string) (bool, error) {
	return f.present[key], nil
}

func (f *drillWalkFixture) skip(runID, reason string) {
	f.skips = append(f.skips, runID+": "+reason)
}

// completeYBRun builds a run manifest and marks every declared object present
// in the fixture. It includes artifacts at the run root and every artifact
// that each node must archive.
func completeYBRun(f *drillWalkFixture, runID string, nodes []string) ybSnapshotManifest {
	manifest := newYBSnapshotManifest(runID, "snap-"+runID, "tack", nodes, ybTestArtifactNames())
	markYBRunArtifactsPresent(f, manifest)

	for _, node := range manifest.Nodes {
		archiveYBNode(f, runID, node)
	}

	return manifest
}

// markYBRunArtifactsPresent marks every declared artifact at the run root as
// published. This models a run after its orchestrator finishes.
func markYBRunArtifactsPresent(f *drillWalkFixture, manifest ybSnapshotManifest) {
	for _, artifact := range manifest.Artifacts {
		f.present[ybRunArtifactKey(manifest.RunID, artifact)] = true
	}
}

// archiveYBNode marks one node's whole archive run present in the fixture.
func archiveYBNode(f *drillWalkFixture, runID string, node ybSnapshotManifestNode) {
	for _, key := range ybNodeArtifactKeys(runID, node) {
		f.present[key] = true
	}
}

// TestNewestCompleteYBSnapshotRunWalksBack checks discovery across older runs.
// Discovery records reasons for skipping the newest run without a manifest and
// the next run with incomplete archives. It selects the newest complete run.
func TestNewestCompleteYBSnapshotRunWalksBack(t *testing.T) {
	fixture := &drillWalkFixture{
		manifests: map[string]ybSnapshotManifest{},
		present:   map[string]bool{},
		skips:     nil,
	}
	complete := completeYBRun(fixture, "run-1", []string{"yb1", "yb2"})
	fixture.manifests["run-1"] = complete
	// run-2 has a manifest and its artifacts, but yb2 has not archived its data.
	incomplete := newYBSnapshotManifest(
		"run-2",
		"snap-run-2",
		"tack",
		[]string{"yb1", "yb2"},
		ybTestArtifactNames(),
	)
	fixture.manifests["run-2"] = incomplete
	markYBRunArtifactsPresent(fixture, incomplete)
	archiveYBNode(fixture, "run-2", incomplete.Nodes[0])
	// The newest run, run-3, does not have a manifest.

	got, found, err := newestCompleteYBSnapshotRun(
		[]string{"run-1", "run-2", "run-3"},
		fixture.fetch,
		fixture.exists,
		fixture.skip,
	)
	if err != nil {
		t.Fatalf("newestCompleteYBSnapshotRun: %v", err)
	}

	if !found {
		t.Fatal("walk must find the complete run-1")
	}

	if got.RunID != "run-1" {
		t.Fatalf("chosen run = %s, want run-1", got.RunID)
	}

	wantSkips := []string{
		"run-3: manifest not uploaded",
		"run-2: nodes yb2 have not uploaded their archives",
	}
	if !reflect.DeepEqual(fixture.skips, wantSkips) {
		t.Fatalf("skips:\n got=%v\nwant=%v", fixture.skips, wantSkips)
	}
}

// TestNewestCompleteYBSnapshotRunPrefersNewest checks that discovery selects
// the newest complete run without checking older runs.
func TestNewestCompleteYBSnapshotRunPrefersNewest(t *testing.T) {
	fixture := &drillWalkFixture{
		manifests: map[string]ybSnapshotManifest{},
		present:   map[string]bool{},
		skips:     nil,
	}
	fixture.manifests["run-1"] = completeYBRun(fixture, "run-1", []string{"yb1"})
	fixture.manifests["run-2"] = completeYBRun(fixture, "run-2", []string{"yb1"})

	got, found, err := newestCompleteYBSnapshotRun(
		[]string{"run-1", "run-2"},
		fixture.fetch,
		fixture.exists,
		fixture.skip,
	)
	if err != nil || !found {
		t.Fatalf("walk: found=%v err=%v", found, err)
	}

	if got.RunID != "run-2" {
		t.Fatalf("chosen run = %s, want the newest complete run-2", got.RunID)
	}

	if len(fixture.skips) != 0 {
		t.Fatalf("skips = %v, want none", fixture.skips)
	}
}

// TestNewestCompleteYBSnapshotRunNoneComplete checks that discovery reports
// that it did not find a complete run. It records a reason for skipping each
// incomplete run.
func TestNewestCompleteYBSnapshotRunNoneComplete(t *testing.T) {
	fixture := &drillWalkFixture{
		manifests: map[string]ybSnapshotManifest{},
		present:   map[string]bool{},
		skips:     nil,
	}
	// run-1's manifest does not list any nodes. run-2 does not have a manifest.
	fixture.manifests["run-1"] = newYBSnapshotManifest(
		"run-1",
		"snap-1",
		"tack",
		nil,
		ybTestArtifactNames(),
	)

	_, found, err := newestCompleteYBSnapshotRun(
		[]string{"run-1", "run-2"},
		fixture.fetch,
		fixture.exists,
		fixture.skip,
	)
	if err != nil {
		t.Fatalf("newestCompleteYBSnapshotRun: %v", err)
	}

	if found {
		t.Fatal("walk must report not found when no run is complete")
	}

	wantSkips := []string{
		"run-2: manifest not uploaded",
		"run-1: manifest lists no tablet-server nodes",
	}
	if !reflect.DeepEqual(fixture.skips, wantSkips) {
		t.Fatalf("skips:\n got=%v\nwant=%v", fixture.skips, wantSkips)
	}
}

// TestNewestCompleteYBSnapshotRunSkipsRunMissingAnArtifact checks that
// discovery requires artifacts at the run root as well as node archives.
// Discovery skips a run without its roles file and identifies the missing
// artifact. Without those roles, a restore cannot provide the privileges that
// applications need to read the ledger. It does not establish a usable
// recovery.
func TestNewestCompleteYBSnapshotRunSkipsRunMissingAnArtifact(t *testing.T) {
	fixture := &drillWalkFixture{
		manifests: map[string]ybSnapshotManifest{},
		present:   map[string]bool{},
		skips:     nil,
	}
	fixture.manifests["run-1"] = completeYBRun(fixture, "run-1", []string{"yb1"})
	partial := completeYBRun(fixture, "run-2", []string{"yb1"})
	fixture.manifests["run-2"] = partial
	delete(fixture.present, ybRunArtifactKey("run-2", ybSnapshotRolesObject))

	got, found, err := newestCompleteYBSnapshotRun(
		[]string{"run-1", "run-2"},
		fixture.fetch,
		fixture.exists,
		fixture.skip,
	)
	if err != nil {
		t.Fatalf("newestCompleteYBSnapshotRun: %v", err)
	}

	if !found || got.RunID != "run-1" {
		t.Fatalf("chosen run = %s (found=%v), want the complete run-1", got.RunID, found)
	}

	wantSkips := []string{"run-2: artifacts roles.sql are absent"}
	if !reflect.DeepEqual(fixture.skips, wantSkips) {
		t.Fatalf("skips:\n got=%v\nwant=%v", fixture.skips, wantSkips)
	}
}

// TestYBDrillManifestDefectRequiresEveryArtifactTheRestoreOpens checks that
// the manifest declares all three artifacts that the restore reads by name.
// The refusal identifies every missing declaration.
//
// A run without artifact declarations predates the roles file. A run with
// only some declarations has an incomplete manifest. Without the roles file,
// restored tables, rows, and row-level security policies lack the privileges
// that the policies require. Without the schema file, the restore lacks the
// schema. Neither case establishes a recovery that applications can read.
func TestYBDrillManifestDefectRequiresEveryArtifactTheRestoreOpens(t *testing.T) {
	tests := []struct {
		name      string
		artifacts []string
		want      string
	}{
		{
			name:      "no artifacts at all",
			artifacts: nil,
			want: "manifest does not declare required artifacts " +
				ybSnapshotMetadataObject + ", " + ybSnapshotSchemaObject + ", " + ybSnapshotRolesObject,
		},
		{
			name:      "only the snapshot metadata",
			artifacts: []string{ybSnapshotMetadataObject},
			want: "manifest does not declare required artifacts " +
				ybSnapshotSchemaObject + ", " + ybSnapshotRolesObject,
		},
		{
			name:      "everything but the roles file",
			artifacts: []string{ybSnapshotMetadataObject, ybSnapshotSchemaObject},
			want:      "manifest does not declare required artifacts " + ybSnapshotRolesObject,
		},
		{
			name:      "what a real export declares",
			artifacts: ybTestArtifactNames(),
			want:      "",
		},
	}
	for _, test := range tests {
		t.Run(
			test.name,
			func(t *testing.T) {
				manifest := newYBSnapshotManifest(
					"run-1",
					"snap-1",
					"tack",
					[]string{"yb1"},
					test.artifacts,
				)
				if defect := ybDrillManifestDefect(manifest); defect != test.want {
					t.Fatalf("defect:\n got=%q\nwant=%q", defect, test.want)
				}
			},
		)
	}
}

// TestNewestCompleteYBSnapshotRunSkipsATruncatedDeclaration checks that
// discovery skips a run with fewer artifact declarations than the restore
// requires, even when every declared object exists.
//
// Checking only declared objects would accept that run. The restore would
// then fail when it opened an undeclared fixed artifact path. The skip reason
// identifies an incomplete manifest, which requires a different repair from
// an artifact that the backup did not upload.
func TestNewestCompleteYBSnapshotRunSkipsATruncatedDeclaration(t *testing.T) {
	fixture := &drillWalkFixture{
		manifests: map[string]ybSnapshotManifest{},
		present:   map[string]bool{},
		skips:     nil,
	}
	fixture.manifests["run-1"] = completeYBRun(fixture, "run-1", []string{"yb1"})
	truncated := newYBSnapshotManifest(
		"run-2",
		"snap-run-2",
		"tack",
		[]string{"yb1"},
		[]string{ybSnapshotMetadataObject},
	)
	fixture.manifests["run-2"] = truncated
	markYBRunArtifactsPresent(fixture, truncated)

	for _, node := range truncated.Nodes {
		archiveYBNode(fixture, "run-2", node)
	}

	got, found, err := newestCompleteYBSnapshotRun(
		[]string{"run-1", "run-2"},
		fixture.fetch,
		fixture.exists,
		fixture.skip,
	)
	if err != nil {
		t.Fatalf("newestCompleteYBSnapshotRun: %v", err)
	}

	if !found || got.RunID != "run-1" {
		t.Fatalf("chosen run = %s (found=%v), want the complete run-1", got.RunID, found)
	}

	wantSkips := []string{
		"run-2: manifest does not declare required artifacts " +
			ybSnapshotSchemaObject + ", " + ybSnapshotRolesObject,
	}
	if !reflect.DeepEqual(fixture.skips, wantSkips) {
		t.Fatalf("skips:\n got=%v\nwant=%v", fixture.skips, wantSkips)
	}
}

// TestNewestCompleteYBSnapshotRunPropagatesFetchErrors checks that a store
// error aborts discovery unless it reports a missing manifest. Discovery must
// not silently select an older run after another store error.
func TestNewestCompleteYBSnapshotRunPropagatesFetchErrors(t *testing.T) {
	fetch := func(string) (ybSnapshotManifest, error) {
		return ybSnapshotManifest{}, fmt.Errorf("store unreachable")
	}
	exists := func(string) (bool, error) { return true, nil }
	_, found, err := newestCompleteYBSnapshotRun(
		[]string{"run-1"},
		fetch,
		exists,
		func(string, string) {},
	)
	if err == nil || found {
		t.Fatalf("walk must propagate the fetch error, got found=%v err=%v", found, err)
	}
}
