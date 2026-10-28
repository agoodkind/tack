package ops_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
)

const (
	// retentionFDBReadLifeKnob and retentionFDBCommitIntervalKnob shorten how
	// long the storage server keeps old versions readable. With them, a
	// describe with --version-timestamps fails on the test cluster after a few
	// hundred snapshots, as it fails on QA after weeks of hourly snapshots.
	retentionFDBReadLifeKnob       = "max_read_transaction_life_versions"
	retentionFDBReadLifeVersions   = "50000"
	retentionFDBCommitIntervalKnob = "storage_commit_interval"
	retentionFDBCommitInterval     = "0.01"
	// retentionFDBTooOldText is the message of FoundationDB error 1007.
	retentionFDBTooOldText = "Transaction is too old"
)

// TestBackupRetentionFDBPlansBackupWithManySnapshots runs retention on a
// backup with enough snapshots that `fdbbackup describe --version-timestamps`
// fails with error 1007 (TACK-609). The preview and the execution must expire
// the backup, and the cutoff must stay restorable.
func TestBackupRetentionFDBPlansBackupWithManySnapshots(t *testing.T) {
	fixture := newRetentionCommandFixture(t)
	cluster := retentionFDBTestConfigWithKnobs(t, fixture.config, map[string]string{
		retentionFDBReadLifeKnob: retentionFDBReadLifeVersions, retentionFDBCommitIntervalKnob: retentionFDBCommitInterval,
	})
	name := retentionFDBTestStartEvery(t, fixture.config, cluster, "1")
	first := retentionFDBTestRecordReading(t, fixture, cluster, time.Time{})
	reading := retentionFDBTestRecordReading(t, fixture, cluster, first.At.Add(10*time.Second))
	cutoff := reading.At.Add(time.Second)
	snapshots := retentionFDBTestAwaitTooOld(t, fixture.config, cluster, name)
	t.Logf("describe --version-timestamps failed with error 1007 after %d snapshots", snapshots)
	retentionFDBTestRecordReading(t, fixture, cluster, cutoff.Add(15*time.Second))
	retentionFDBTestWindow(t, fixture.config, cluster, name, reading.Version)
	ops.SetBackupRetentionTestClock(t, cutoff.AddDate(0, 0, 14))
	before := retentionFDBTestKeys(t, fixture)
	output, err := fixture.run(t, false)
	if err != nil {
		t.Fatalf("public preview did not plan a backup with %d snapshots: %v\n%s", snapshots, err, output)
	}
	action := retentionFDBTestPreviewAction(t, output)
	if action.Name != name || action.Action != "expire" || action.ExpireBeforeVersion <= 0 || action.Window.MaxVersion < reading.Version {
		t.Fatalf("public preview did not select native expiration: %+v", action)
	}
	if output, err := fixture.run(t, true); err != nil {
		t.Fatalf("public retention did not expire a backup with %d snapshots: %v\n%s", snapshots, err, output)
	}
	after := retentionFDBTestKeys(t, fixture)
	removed := 0
	for key := range before {
		if _, found := after[key]; !found {
			removed++
		}
	}
	if removed == 0 {
		t.Fatal("native expiration did not remove any existing backup objects")
	}
	retentionFDBTestRestore(t, fixture.config, cluster, name, cutoff)
}

// retentionFDBTestAwaitTooOld runs describe with --version-timestamps until it
// fails with error 1007, and returns the number of snapshots in the backup at
// that point.
func retentionFDBTestAwaitTooOld(t *testing.T, cfg *config.Config, cluster, name string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	describe := []string{"fdbbackup", "describe", "-d", retentionFDBTestURL(cfg, name), "-C", "/var/fdb/fdb.cluster"}
	for {
		output, exitCode := retentionFDBTestExecResult(t, cfg, cluster, append(describe, "--version-timestamps"))
		if exitCode != 0 {
			if !strings.Contains(output, retentionFDBTooOldText) {
				t.Fatalf("describe --version-timestamps exited %d without error 1007: %s", exitCode, output)
			}
			return strings.Count(retentionFDBTestExec(t, cfg, cluster, describe), "\nSnapshot:")
		}
		select {
		case <-ctx.Done():
			t.Fatalf("describe --version-timestamps did not fail with error 1007 after %d snapshots",
				strings.Count(output, "\nSnapshot:"))
		case <-ticker.C:
		}
	}
}

// retentionFDBTestPreviewAction returns the one FoundationDB action of the
// JSON plan that a public preview prints.
func retentionFDBTestPreviewAction(t *testing.T, output string) retentionFDBPreviewAction {
	t.Helper()
	jsonPlan := ""
	for line := range strings.SplitSeq(output, "\n") {
		if strings.HasPrefix(line, "{") {
			if jsonPlan != "" {
				t.Fatal("public preview printed multiple JSON plans")
			}
			jsonPlan = line
		}
	}
	var preview retentionFDBPreview
	if err := json.Unmarshal([]byte(jsonPlan), &preview); err != nil {
		t.Fatalf("public preview did not print a JSON plan: %v\n%s", err, output)
	}
	if len(preview.FoundationDB) != 1 {
		t.Fatalf("public preview returned %d FDB actions", len(preview.FoundationDB))
	}
	return preview.FoundationDB[0]
}
