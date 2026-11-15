package ops

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"
)

// refusingWriter simulates failed stdout writes under a service manager.
type refusingWriter struct{ err error }

func (w refusingWriter) Write([]byte) (int, error) { return 0, w.err }

// TestBackupAlarmHandlesReportWriteFailure checks email and stale-verdict
// precedence when stdout fails. Fresh runs must instead return the write error
// without sending email.
func TestBackupAlarmHandlesReportWriteFailure(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	writeErr := errors.New("write /dev/stdout: broken pipe")
	out := refusingWriter{err: writeErr}

	staleCfg := unreachableBackupStalenessConfig(t, "backups@example.test")
	err := RunBackupStalenessCheck(context.Background(), staleCfg, out)
	if err == nil {
		t.Fatal("a stale run must exit nonzero")
	}

	if !strings.Contains(err.Error(), "past threshold") || errors.Is(err, writeErr) {
		t.Fatalf("stale runs must return the stale verdict instead of the write error: %v", err)
	}

	if len(captured.messages) != 1 {
		t.Fatalf(
			"report failure must not prevent fault email, sent %d",
			len(captured.messages),
		)
	}

	if alarmed, found := alarmedBackupMetrics(t, staleCfg); !found || len(alarmed) != 3 {
		t.Fatalf("state must record acceptance, found = %v state = %v", found, alarmed)
	}

	objects := ybExportRunObjects(
		t,
		"20260829T100000Z",
		newYBSnapshotManifest(
			"20260829T100000Z",
			"snap-1",
			"tack",
			[]string{"yb1"},
			ybTestArtifactNames(),
		),
	)
	maps.Copy(
		objects,
		map[string][]byte{
			backupStatusKey(backupStalenessRehearsalName): marshalBackupStatusMarker(
				t,
				now.Add(-6*time.Hour),
				"restore drill passed every leg",
			),
			backupStatusKey(backupStalenessReplicationName): marshalBackupStatusMarker(
				t,
				now.Add(-10*time.Minute),
				"0 dead nodes, 0 under-replicated tablets",
			),
		},
	)
	freshCfg := storedBackupStalenessConfig(t, newBackupTestStore(t, objects))
	seesHealthyCluster(t, freshCfg)
	err = RunBackupStalenessCheck(context.Background(), freshCfg, out)
	if !errors.Is(err, writeErr) {
		t.Fatalf(
			"fresh runs must return the report write error, got %v",
			err,
		)
	}

	if len(captured.messages) != 1 {
		t.Fatalf("a fresh run must not send email, sent %d total", len(captured.messages))
	}
}
