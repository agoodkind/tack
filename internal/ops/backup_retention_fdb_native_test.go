//go:build integration

package ops_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/ops"
)

type retentionFDBPreview struct {
	FoundationDB []retentionFDBPreviewAction `json:"foundationdb"`
}

type retentionFDBPreviewAction struct {
	Name                string             `json:"name"`
	Action              string             `json:"action"`
	Window              retentionFDBWindow `json:"window"`
	ExpireBeforeVersion int64              `json:"expire_before_version"`
}

func TestBackupRetentionFDBNativeExpirationRetainsBoundary(t *testing.T) {
	fixture := newRetentionCommandFixture(t)
	cluster := retentionFDBTestConfig(t, fixture.config)
	name := retentionFDBTestStart(t, fixture.config, cluster)
	first := retentionFDBTestRecordReading(t, fixture, cluster, time.Time{})
	reading := retentionFDBTestRecordReading(t, fixture, cluster, first.At.Add(75*time.Second))
	cutoff := reading.At.Add(time.Second)
	retentionFDBTestRecordReading(t, fixture, cluster, cutoff.Add(15*time.Second))
	window := retentionFDBTestWindow(t, fixture.config, cluster, name, reading.Version)
	ops.SetBackupRetentionTestClock(t, cutoff.AddDate(0, 0, 14))
	before := retentionFDBTestKeys(t, fixture)
	stopMetadata := observeRetentionMetadata(t, fixture.config)
	output, err := fixture.run(t, false)
	if err != nil {
		t.Fatalf("public preview did not select native expiration: %v\n%s", err, output)
	}
	afterPreview := retentionFDBTestKeys(t, fixture)
	for key := range before {
		if _, found := afterPreview[key]; !found {
			t.Fatalf("public preview deleted existing backup object %s", key)
		}
	}
	action := retentionFDBTestPreviewAction(t, output)
	if action.Name != name || action.Action != "expire" || action.ExpireBeforeVersion <= 0 ||
		action.Window.MinVersion != window.MinVersion || action.Window.MaxVersion < reading.Version {
		t.Fatalf("public preview omitted safe native selection evidence: %+v", action)
	}
	t.Logf("selected snapshot begin version %d for retained cutoff %s", action.ExpireBeforeVersion, cutoff)
	description := retentionFDBTestExec(t, fixture.config, cluster, []string{"fdbbackup", "describe", "-d", retentionFDBTestURL(fixture.config, name), "-C", "/var/fdb/fdb.cluster"})
	for line := range strings.SplitSeq(description, "\n") {
		if strings.HasPrefix(line, "Snapshot:") || strings.HasPrefix(line, "MinLogBeginVersion:") || strings.HasPrefix(line, "ContiguousLogEndVersion:") {
			t.Log(line)
		}
	}
	if output, err := fixture.run(t, true); err != nil {
		t.Fatalf("public retention did not complete native expiration: %v\n%s", err, output)
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
	metadata := stopMetadata()
	if metadata.URLCommands == 0 || len(metadata.Problems) != 0 {
		t.Fatalf("native command metadata verification failed: URL commands=%d problems=%v", metadata.URLCommands, metadata.Problems)
	}
	stepResults, terminalResults := 0, 0
	for _, event := range fixture.events(t) {
		if event.Context.Phase != audit.PhaseResult {
			continue
		}
		if event.Outcome != audit.OutcomeCompleted {
			t.Fatalf("native expiration recorded outcome %s", event.Outcome)
		}
		if event.Context.ParentOperationID == uuid.Nil {
			terminalResults++
			continue
		}
		var evidence retentionFDBPreviewAction
		if err := json.Unmarshal(event.Extra, &evidence); err != nil {
			t.Fatal(err)
		}
		if evidence.ExpireBeforeVersion != action.ExpireBeforeVersion || evidence.Action != "expire" ||
			evidence.Window.MinVersion != action.Window.MinVersion || evidence.Window.MaxVersion < reading.Version {
			t.Fatalf("audit result differs from selected native expiration: %s", event.Extra)
		}
		stepResults++
	}
	if stepResults != 1 || terminalResults != 1 {
		t.Fatalf("native expiration recorded %d step results and %d terminal results", stepResults, terminalResults)
	}
	retentionFDBTestRestore(t, fixture.config, cluster, name, cutoff)
	status := retentionFDBTestExec(t, fixture.config, cluster, []string{"fdbbackup", "status", "-C", "/var/fdb/fdb.cluster"})
	if !strings.Contains(status, "restorable but continuing") {
		t.Fatal("native expiration did not preserve the active session")
	}
}

func TestBackupRetentionFDBNativeRefusalPreservesBackup(t *testing.T) {
	fixture := newRetentionCommandFixture(t)
	cluster := retentionFDBTestConfig(t, fixture.config)
	name := retentionFDBTestStart(t, fixture.config, cluster)
	first := retentionFDBTestRecordReading(t, fixture, cluster, time.Time{})
	reading := retentionFDBTestRecordReading(t, fixture, cluster, first.At.Add(15*time.Second))
	cutoff := reading.At.Add(time.Second)
	retentionFDBTestRecordReading(t, fixture, cluster, cutoff.Add(10*time.Second))
	retentionFDBTestWindow(t, fixture.config, cluster, name, reading.Version)
	before := retentionFDBTestKeys(t, fixture)
	retentionFDBTestRefusal(t, fixture.config, cluster, name, cutoff)
	after := retentionFDBTestKeys(t, fixture)
	for key := range before {
		if _, found := after[key]; !found {
			t.Fatalf("native refusal deleted existing object %s", key)
		}
	}
	ops.SetBackupRetentionTestClock(t, cutoff.AddDate(0, 0, 14))
	if output, err := fixture.run(t, false); err != nil || !strings.Contains(output, name) {
		t.Fatalf("public preview cannot inspect the preserved backup: %v\n%s", err, output)
	}
	retentionFDBTestRestore(t, fixture.config, cluster, name, cutoff)
}

func TestBackupRetentionFDBDeletesClosedSessionPreservesSurvivor(t *testing.T) {
	fixture := newRetentionCommandFixture(t)
	cluster := retentionFDBTestConfig(t, fixture.config)
	oldName := retentionFDBTestStart(t, fixture.config, cluster)
	retentionFDBTestWindow(t, fixture.config, cluster, oldName, 0)
	retentionFDBTestExec(t, fixture.config, cluster, []string{"fdbbackup", "discontinue", "-C", "/var/fdb/fdb.cluster", "-w"})
	retentionFDBTestWindow(t, fixture.config, cluster, oldName, 0)
	newName := retentionFDBTestStart(t, fixture.config, cluster)
	first := retentionFDBTestRecordReading(t, fixture, cluster, time.Time{})
	reading := retentionFDBTestRecordReading(t, fixture, cluster, first.At.Add(10*time.Second))
	cutoff := reading.At.Add(time.Second)
	retentionFDBTestRecordReading(t, fixture, cluster, cutoff.Add(10*time.Second))
	retentionFDBTestWindow(t, fixture.config, cluster, newName, reading.Version)
	ops.SetBackupRetentionTestClock(t, cutoff.AddDate(0, 0, 14))
	if output, err := fixture.run(t, true); err != nil {
		t.Fatalf("public retention did not delete the closed session: %v\n%s", err, output)
	}
	fixture.requireObject(t, "backups/"+oldName, false)
	fixture.requireObject(t, "backups/"+newName, true)
	retentionFDBTestRestore(t, fixture.config, cluster, newName, cutoff)
}
