package ops_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/audit"
)

func TestBackupRetentionPreviewDeleteAndRepeat(t *testing.T) {
	fixture := newRetentionCommandFixture(t)
	expired := fixture.completeRun(t, fixture.cutoff.Add(-time.Second))
	boundary := fixture.completeRun(t, fixture.cutoff)
	recent := fixture.completeRun(t, fixture.cutoff.Add(time.Hour))
	status := "backup-status/fdb-versions.json"
	unrelated := "other/20200101T000000Z/data"
	fixture.store.put(status, []byte("status"))
	fixture.store.put(unrelated, []byte("other"))
	incomplete := fixture.cutoff.Add(-time.Hour).Format(ybSnapshotRunIDLayout)
	fixture.store.put(ybRunArtifactKey(incomplete, "partial"), []byte("partial export"))
	output, err := fixture.run(t, false)
	if err != nil || !strings.Contains(output, expired) {
		t.Fatalf("preview did not select expired run: %v\n%s", err, output)
	}
	var preview struct {
		Cutoff       time.Time         `json:"cutoff"`
		LedgerRuns   []string          `json:"ledger_runs"`
		FoundationDB []json.RawMessage `json:"foundationdb"`
	}
	start := strings.Index(output, "{")
	if start < 0 {
		t.Fatalf("preview did not print a JSON selection: %s", output)
	}
	decoder := json.NewDecoder(strings.NewReader(output[start:]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&preview); err != nil {
		t.Fatalf("decode preview selection: %v", err)
	}
	if !preview.Cutoff.Equal(fixture.cutoff) || len(preview.LedgerRuns) != 2 || len(preview.FoundationDB) != 0 {
		t.Fatalf("preview default restore window or selected targets differ: %+v", preview)
	}
	fixture.requireObject(t, ybRunArtifactKey(expired, ybSnapshotManifestObject), true)
	if events := fixture.events(t); len(events) != 0 {
		t.Fatalf("preview recorded %d execution events", len(events))
	}
	if output, err := fixture.run(t, true); err != nil {
		t.Fatalf("execute retention: %v\n%s", err, output)
	}
	fixture.requireObject(t, ybRunArtifactKey(expired, ybSnapshotManifestObject), false)
	fixture.requireObject(t, ybRunArtifactKey(incomplete, "partial"), false)
	for _, run := range []string{boundary, recent} {
		fixture.requireObject(t, ybRunArtifactKey(run, ybSnapshotManifestObject), true)
	}
	for _, key := range []string{status, unrelated} {
		fixture.requireObject(t, key, true)
	}
	events := fixture.events(t)
	intents, results, deleted := 0, 0, 0
	for _, event := range events {
		if event.Context.Phase == audit.PhaseIntent {
			intents++
		}
		if event.Context.Phase == audit.PhaseResult && event.Outcome == audit.OutcomeCompleted {
			results++
		}
		if strings.Contains(string(event.Extra), "deleted_objects") && event.Context.Phase == audit.PhaseResult {
			deleted++
		}
	}
	if intents != 3 || results != 3 || deleted != 2 {
		t.Fatalf("audit recorded intents=%d completed=%d deletion_results=%d; expected 3, 3, 2", intents, results, deleted)
	}
	if output, err := fixture.run(t, true); err != nil {
		t.Fatalf("repeat retention: %v\n%s", err, output)
	}
}

func TestBackupRetentionRequiresCompleteRetainedExport(t *testing.T) {
	fixture := newRetentionCommandFixture(t)
	expired := fixture.completeRun(t, fixture.cutoff.Add(-time.Hour))
	recent := fixture.completeRun(t, fixture.cutoff.Add(time.Hour))
	fixture.store.remove(ybSnapshotKeyPrefix(recent) + "nodes/yb1/" + ybNodeInventoryObject)
	if output, err := fixture.run(t, true); err == nil || !strings.Contains(err.Error(), "complete export") {
		t.Fatalf("retention did not refuse missing retained complete export: %v\n%s", err, output)
	}
	fixture.requireObject(t, ybRunArtifactKey(expired, ybSnapshotManifestObject), true)
	for _, event := range fixture.events(t) {
		if event.Context.Phase == audit.PhaseResult && event.Outcome != audit.OutcomeRefused {
			t.Fatalf("guard failure recorded outcome %s instead of refused", event.Outcome)
		}
	}
}

func TestBackupRetentionRefusesMalformedScope(t *testing.T) {
	for _, malformed := range []string{"not-a-run", "20261301T000000Z"} {
		t.Run(malformed, func(t *testing.T) {
			fixture := newRetentionCommandFixture(t)
			expired := fixture.completeRun(t, fixture.cutoff.Add(-time.Hour))
			fixture.completeRun(t, fixture.cutoff.Add(time.Hour))
			key := "yugabyte-snapshot/" + malformed + "/data"
			fixture.store.put(key, []byte("invalid scope"))
			if _, err := fixture.run(t, true); err == nil {
				t.Fatal("retention accepted an invalid run identifier")
			}
			fixture.requireObject(t, ybRunArtifactKey(expired, ybSnapshotManifestObject), true)
			fixture.requireObject(t, key, true)
		})
	}
}

func TestBackupRetentionRefusesMalformedManifest(t *testing.T) {
	fixture := newRetentionCommandFixture(t)
	expired := fixture.completeRun(t, fixture.cutoff.Add(-time.Hour))
	fixture.completeRun(t, fixture.cutoff.Add(time.Hour))
	fixture.store.put(ybRunArtifactKey(expired, ybSnapshotManifestObject), []byte(`{"run_id":"../../other"}`))
	if _, err := fixture.run(t, true); err == nil {
		t.Fatal("retention accepted an invalid manifest")
	}
	fixture.requireObject(t, ybRunArtifactKey(expired, ybSnapshotSchemaObject), true)
}
