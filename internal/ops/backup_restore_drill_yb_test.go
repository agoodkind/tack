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
// declares present in the fixture: each run-root artifact and each node
// archive.
func completeYBRun(f *drillWalkFixture, runID string, nodes []string) ybSnapshotManifest {
	manifest := newYBSnapshotManifest(runID, "snap-"+runID, "tack", nodes, ybTestArtifactNames())
	markYBRunArtifactsPresent(f, manifest)
	for _, node := range manifest.Nodes {
		f.present[ybNodeArchiveKey(runID, node)] = true
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
	fixture.present[ybNodeArchiveKey("run-2", incomplete.Nodes[0])] = true
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

// TestYBDrillManifestDefectRefusesAManifestWithoutArtifacts proves an export
// written before the run carried its own roles file is refused rather than
// drilled. Such a run restores tables, rows, and row-level-security policies
// with none of the privileges those policies depend on, so a drill that passed
// on it would report a recovery nobody can actually read.
func TestYBDrillManifestDefectRefusesAManifestWithoutArtifacts(t *testing.T) {
	artifactless := newYBSnapshotManifest("run-1", "snap-1", "tack", []string{"yb1"}, nil)
	if defect := ybDrillManifestDefect(artifactless); defect != "manifest lists no run artifacts" {
		t.Fatalf("defect = %q, want the missing-artifacts refusal", defect)
	}

	complete := newYBSnapshotManifest("run-1", "snap-1", "tack", []string{"yb1"}, ybTestArtifactNames())
	if defect := ybDrillManifestDefect(complete); defect != "" {
		t.Fatalf("a manifest declaring its artifacts must be drillable, got %q", defect)
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
