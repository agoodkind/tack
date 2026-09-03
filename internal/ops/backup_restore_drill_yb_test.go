package ops

import (
	"fmt"
	"reflect"
	"testing"

	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// drillWalkFixture builds the walk-back callbacks from a run-id-keyed manifest
// set and a set of present archive keys, recording every skip.
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

// completeYBRun builds a manifest for the run and marks every object it
// declares present in the fixture: each run-root artifact and every artifact
// every node owes.
func completeYBRun(f *drillWalkFixture, runID string, nodes []string) ybSnapshotManifest {
	manifest := newYBSnapshotManifest(runID, "snap-"+runID, "tack", nodes, ybTestArtifactNames())
	markYBRunArtifactsPresent(f, manifest)
	for _, node := range manifest.Nodes {
		archiveYBNode(f, runID, node)
	}
	return manifest
}

// markYBRunArtifactsPresent marks every run-root artifact the manifest declares
// as published, which is what a run whose orchestrator finished looks like.
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

// TestNewestCompleteYBSnapshotRunWalksBack proves the drill's discovery walk:
// a manifest-less newest run and an incomplete second run are skipped with
// reasons, and the newest complete run wins.
func TestNewestCompleteYBSnapshotRunWalksBack(t *testing.T) {
	fixture := &drillWalkFixture{
		manifests: map[string]ybSnapshotManifest{},
		present:   map[string]bool{},
		skips:     nil,
	}
	complete := completeYBRun(fixture, "run-1", []string{"yb1", "yb2"})
	fixture.manifests["run-1"] = complete
	// run-2 has a manifest and its artifacts but yb2 has not archived.
	incomplete := newYBSnapshotManifest("run-2", "snap-run-2", "tack",
		[]string{"yb1", "yb2"}, ybTestArtifactNames())
	fixture.manifests["run-2"] = incomplete
	markYBRunArtifactsPresent(fixture, incomplete)
	archiveYBNode(fixture, "run-2", incomplete.Nodes[0])
	// run-3 (the newest) has no manifest at all.

	got, found, err := newestCompleteYBSnapshotRun(
		[]string{"run-1", "run-2", "run-3"}, fixture.fetch, fixture.exists, fixture.skip)
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

// TestNewestCompleteYBSnapshotRunPrefersNewest proves the walk stops at the
// newest complete run without touching older ones.
func TestNewestCompleteYBSnapshotRunPrefersNewest(t *testing.T) {
	fixture := &drillWalkFixture{
		manifests: map[string]ybSnapshotManifest{},
		present:   map[string]bool{},
		skips:     nil,
	}
	fixture.manifests["run-1"] = completeYBRun(fixture, "run-1", []string{"yb1"})
	fixture.manifests["run-2"] = completeYBRun(fixture, "run-2", []string{"yb1"})

	got, found, err := newestCompleteYBSnapshotRun(
		[]string{"run-1", "run-2"}, fixture.fetch, fixture.exists, fixture.skip)
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

// TestNewestCompleteYBSnapshotRunNoneComplete proves the walk reports not
// found, with every run skipped for its own reason, when no run is complete.
func TestNewestCompleteYBSnapshotRunNoneComplete(t *testing.T) {
	fixture := &drillWalkFixture{
		manifests: map[string]ybSnapshotManifest{},
		present:   map[string]bool{},
		skips:     nil,
	}
	// run-1's manifest lists no nodes; run-2 has no manifest.
	fixture.manifests["run-1"] = newYBSnapshotManifest("run-1", "snap-1", "tack", nil, ybTestArtifactNames())

	_, found, err := newestCompleteYBSnapshotRun(
		[]string{"run-1", "run-2"}, fixture.fetch, fixture.exists, fixture.skip)
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

// TestNewestCompleteYBSnapshotRunSkipsRunMissingAnArtifact proves the run-root
// artifacts are gated exactly like the node archives: a run whose roles file
// never landed restores into a database whose ledger no application role can
// read, so discovery walks past it, naming the artifact, rather than drilling
// it and calling the result a rehearsed recovery.
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
		[]string{"run-1", "run-2"}, fixture.fetch, fixture.exists, fixture.skip)
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

// TestYBDrillManifestDefectRequiresEveryArtifactTheRestoreOpens proves a
// manifest is refused unless it declares all three artifacts the restore reads
// by name, and that the refusal names the ones it left out.
//
// A run declaring none predates the roles file; a run declaring some was
// truncated. Either restores tables, rows, and row-level-security policies with
// none of the privileges those policies depend on, or with no schema at all, so
// a drill that passed on one would report a recovery nobody can actually read.
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
		t.Run(test.name, func(t *testing.T) {
			manifest := newYBSnapshotManifest("run-1", "snap-1", "tack", []string{"yb1"}, test.artifacts)
			if defect := ybDrillManifestDefect(manifest); defect != test.want {
				t.Fatalf("defect:\n got=%q\nwant=%q", defect, test.want)
			}
		})
	}
}

// TestNewestCompleteYBSnapshotRunSkipsATruncatedDeclaration proves discovery
// walks past a run that declares fewer artifacts than the restore reads, even
// though every object that run declared is present.
//
// This is the gap a declared-complete gate leaves open. Such a run passes every
// presence check, because a presence check can only probe for what was
// declared, so it would be selected as the newest complete run and then fail
// deep in the restore on a fixed path nothing ever gated. The skip reason says
// the manifest is at fault, which is a different repair from an object that did
// not land.
func TestNewestCompleteYBSnapshotRunSkipsATruncatedDeclaration(t *testing.T) {
	fixture := &drillWalkFixture{
		manifests: map[string]ybSnapshotManifest{},
		present:   map[string]bool{},
		skips:     nil,
	}
	fixture.manifests["run-1"] = completeYBRun(fixture, "run-1", []string{"yb1"})
	truncated := newYBSnapshotManifest("run-2", "snap-run-2", "tack",
		[]string{"yb1"}, []string{ybSnapshotMetadataObject})
	fixture.manifests["run-2"] = truncated
	markYBRunArtifactsPresent(fixture, truncated)
	for _, node := range truncated.Nodes {
		archiveYBNode(fixture, "run-2", node)
	}

	got, found, err := newestCompleteYBSnapshotRun(
		[]string{"run-1", "run-2"}, fixture.fetch, fixture.exists, fixture.skip)
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

// TestNewestCompleteYBSnapshotRunPropagatesFetchErrors proves a store error
// other than a missing manifest aborts the walk instead of silently demoting
// the drill to an older run.
func TestNewestCompleteYBSnapshotRunPropagatesFetchErrors(t *testing.T) {
	fetch := func(string) (ybSnapshotManifest, error) {
		return ybSnapshotManifest{}, fmt.Errorf("store unreachable")
	}
	exists := func(string) (bool, error) { return true, nil }
	_, found, err := newestCompleteYBSnapshotRun([]string{"run-1"}, fetch, exists,
		func(string, string) {})
	if err == nil || found {
		t.Fatalf("walk must propagate the fetch error, got found=%v err=%v", found, err)
	}
}
