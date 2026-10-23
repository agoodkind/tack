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
	window := retentionFDBTestWindow(t, fixture.config, cluster, name, time.Time{})
	cutoff := window.Min.Add(75 * time.Second)
	retentionFDBTestWindow(t, fixture.config, cluster, name, cutoff.Add(15*time.Second))
	ops.SetBackupRetentionTestClock(t, cutoff.AddDate(0, 0, 14))
	before := retentionFDBTestKeys(t, fixture)
	output, err := fixture.run(t, false)
	if err != nil {
		t.Fatalf("public preview did not select native expiration: %v\n%s", err, output)
	}
	var preview retentionFDBPreview
	jsonPlan := ""
	for line := range strings.SplitSeq(output, "\n") {
		if strings.HasPrefix(line, "{") {
			if jsonPlan != "" {
				t.Fatal("public preview printed multiple JSON plans")
			}
			jsonPlan = line
		}
	}
	if err := json.Unmarshal([]byte(jsonPlan), &preview); err != nil {
		t.Fatalf("public preview did not print a JSON plan: %v\n%s", err, output)
	}
	if len(preview.FoundationDB) != 1 {
		t.Fatalf("public preview returned %d FDB actions", len(preview.FoundationDB))
	}
	action := preview.FoundationDB[0]
	if action.Name != name || action.Action != "expire" || action.ExpireBeforeVersion <= 0 || !action.Window.Min.Equal(window.Min) || action.Window.Max.Before(cutoff) {
		t.Fatalf("public preview omitted safe native selection evidence: %+v", action)
	}
	t.Logf("selected snapshot begin version %d for retained cutoff %s", action.ExpireBeforeVersion, cutoff)
	description := retentionFDBTestExec(t, fixture.config, cluster, []string{"fdbbackup", "describe", "-d", retentionFDBTestURL(fixture.config, name), "-C", "/var/fdb/fdb.cluster", "--version-timestamps"})
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
		if evidence.ExpireBeforeVersion != action.ExpireBeforeVersion || evidence.Action != "expire" {
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
	window := retentionFDBTestWindow(t, fixture.config, cluster, name, time.Time{})
	cutoff := window.Min.Add(15 * time.Second)
	retentionFDBTestWindow(t, fixture.config, cluster, name, cutoff.Add(10*time.Second))
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
	retentionFDBTestWindow(t, fixture.config, cluster, oldName, time.Time{})
	retentionFDBTestExec(t, fixture.config, cluster, []string{"fdbbackup", "discontinue", "-C", "/var/fdb/fdb.cluster", "-w"})
	oldWindow := retentionFDBTestWindow(t, fixture.config, cluster, oldName, time.Time{})
	newName := retentionFDBTestStart(t, fixture.config, cluster)
	newWindow := retentionFDBTestWindow(t, fixture.config, cluster, newName, oldWindow.Max.Add(20*time.Second))
	cutoff := newWindow.Min.Add(10 * time.Second)
	ops.SetBackupRetentionTestClock(t, cutoff.AddDate(0, 0, 14))
	if output, err := fixture.run(t, true); err != nil {
		t.Fatalf("public retention did not delete the closed session: %v\n%s", err, output)
	}
	fixture.requireObject(t, "backups/"+oldName, false)
	fixture.requireObject(t, "backups/"+newName, true)
	retentionFDBTestRestore(t, fixture.config, cluster, newName, cutoff)
}
