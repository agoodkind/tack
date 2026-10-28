package ops_test

import (
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/ops"
)

func TestBackupRetentionFDBRefusesSurvivorAfterCutoff(t *testing.T) {
	fixture := newRetentionCommandFixture(t)
	cluster := retentionFDBTestConfig(t, fixture.config)
	oldName := retentionFDBTestStart(t, fixture.config, cluster)
	retentionFDBTestWindow(t, fixture.config, cluster, oldName, 0)
	retentionFDBTestExec(t, fixture.config, cluster, []string{"fdbbackup", "discontinue", "-C", "/var/fdb/fdb.cluster", "-w"})
	oldWindow := retentionFDBTestWindow(t, fixture.config, cluster, oldName, 0)
	// Record a version log reading between the two sessions, which the native
	// conversion places after the old session's last restorable version.
	reading := retentionFDBTestRecordMoment(t, fixture, cluster, oldName, time.Now().UTC().Truncate(time.Second).Add(time.Second))
	if reading.Version <= oldWindow.MaxVersion {
		t.Fatalf("the reading must follow the old session: reading=%d max=%d", reading.Version, oldWindow.MaxVersion)
	}
	cutoff := reading.At.Add(time.Second)
	// Create a real interval without a backup between the two sessions.
	delay := time.Until(cutoff.Add(20 * time.Second))
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-t.Context().Done():
			t.Fatal("the test ended while establishing the interval between backups")
		case <-timer.C:
		}
	}
	newName := retentionFDBTestStart(t, fixture.config, cluster)
	newWindow := retentionFDBTestWindow(t, fixture.config, cluster, newName, 0)
	if newWindow.MinVersion <= reading.Version {
		t.Fatalf("the new session must begin after the uncovered cutoff: min=%d reading=%d", newWindow.MinVersion, reading.Version)
	}
	ops.SetBackupRetentionTestClock(t, cutoff.AddDate(0, 0, 14))
	before := retentionFDBTestKeys(t, fixture)
	if output, err := fixture.run(t, true); err == nil || !strings.Contains(err.Error(), "verified newer retained survivor") {
		t.Fatalf("retention did not refuse a survivor starting after cutoff: %v\n%s", err, output)
	}
	after := retentionFDBTestKeys(t, fixture)
	for key := range before {
		if _, exists := after[key]; !exists {
			t.Fatalf("retention removed backup object %s despite the uncovered cutoff", key)
		}
	}
	fixture.requireObject(t, "backups/"+oldName, true)
	results := 0
	for _, event := range fixture.events(t) {
		if event.Context.Phase == audit.PhaseResult {
			results++
			if event.Outcome != audit.OutcomeRefused {
				t.Fatalf("the uncovered cutoff recorded outcome %s", event.Outcome)
			}
		}
	}
	if results != 1 {
		t.Fatalf("the uncovered cutoff recorded %d results; expected one command refusal", results)
	}
}
