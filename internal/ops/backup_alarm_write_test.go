package ops

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"
)

// refusingWriter is an output stream that accepts nothing, the shape of a
// closed stdout under a service manager.
type refusingWriter struct{ err error }

func (w refusingWriter) Write([]byte) (int, error) { return 0, w.err }

// TestBackupStalenessAlarmMailsWhenTheReportCannotBeWritten pins that the mail
// does not depend on stdout. A stale reading whose report write fails still
// mails once and still returns the stale verdict, not the write error; a fresh
// reading whose report write fails returns the write error and mails nothing.
func TestBackupStalenessAlarmMailsWhenTheReportCannotBeWritten(t *testing.T) {
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
		t.Fatalf("a stale run must return the staleness verdict, not the write error: %v", err)
	}
	if len(captured.messages) != 1 {
		t.Fatalf("the fault must mail even though the report could not be written, sent %d", len(captured.messages))
	}
	if alarmed, found := alarmedBackupMetrics(t, staleCfg); !found || len(alarmed) != 3 {
		t.Fatalf("an accepted mail must be remembered, found = %v state = %v", found, alarmed)
	}

	objects := fakeYBExportRunObjects(t, "20260829T100000Z",
		newYBSnapshotManifest("20260829T100000Z", "snap-1", "tack", []string{"yb1"}))
	maps.Copy(objects, map[string][]byte{
		backupStatusKey(backupStalenessRehearsalName): marshalBackupStatusMarker(t,
			now.Add(-6*time.Hour), "restore drill passed every leg"),
		backupStatusKey(backupStalenessReplicationName): marshalBackupStatusMarker(t,
			now.Add(-10*time.Minute), "0 dead nodes, 0 under-replicated tablets"),
	})
	freshCfg := storedBackupStalenessConfig(t, objects)
	err = RunBackupStalenessCheck(context.Background(), freshCfg, out)
	if !errors.Is(err, writeErr) {
		t.Fatalf("a fresh run whose report cannot be written must return the write error, got %v", err)
	}
	if len(captured.messages) != 1 {
		t.Fatalf("a fresh run must mail nothing, sent %d in total", len(captured.messages))
	}
}
