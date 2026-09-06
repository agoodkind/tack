package ops

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// deputyBackupStalenessConfig is an unreachable-store host that defers to the
// primary at primaryURL.
func deputyBackupStalenessConfig(t *testing.T, primaryURL string) *config.Config {
	t.Helper()
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")
	cfg.BackupAlarmPrimaryURL = primaryURL
	return cfg
}

// runStaleDeputyCheck runs the command once through a context whose logger
// writes to the returned buffer, and asserts the stale verdict came back.
func runStaleDeputyCheck(t *testing.T, cfg *config.Config) string {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
		AddSource: false, Level: slog.LevelDebug, ReplaceAttr: nil,
	}))
	ctx := telemetry.WithLogger(context.Background(), logger)
	var out bytes.Buffer
	err := RunBackupStalenessCheck(ctx, cfg, &out)
	if err == nil {
		t.Fatal("a stale run must exit nonzero")
	}
	if !strings.Contains(err.Error(), "past threshold") {
		t.Fatalf("the returned error must be the staleness verdict: %v", err)
	}
	return logs.String()
}

// TestBackupStalenessDeputyDefersWhileThePrimaryAnswers is the one-mail rule
// across guests: a deputy whose primary answers leaves a new fault unmailed and
// unrecorded, on the first run and on every run after it.
func TestBackupStalenessDeputyDefersWhileThePrimaryAnswers(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(primary.Close)
	cfg := deputyBackupStalenessConfig(t, primary.URL)

	for run := 1; run <= 2; run++ {
		logs := runStaleDeputyCheck(t, cfg)
		if len(captured.messages) != 0 {
			t.Fatalf("run %d: a deputy whose primary answers must not mail, sent %d", run, len(captured.messages))
		}
		if _, found := alarmedBackupMetrics(t, cfg); found {
			t.Fatalf("run %d: a deferred fault must not be recorded", run)
		}
		if !strings.Contains(logs, "msg=backup.staleness.alarm_deferred") ||
			!strings.Contains(logs, backupStalenessExportName) ||
			!strings.Contains(logs, "primary="+primary.URL) {
			t.Fatalf("run %d: the deferral must be logged with the metrics and the primary:\n%s", run, logs)
		}
	}
}

// TestBackupStalenessDeputyMailsWhenThePrimaryIsGone covers the deputy's
// reason to exist: with the primary's port closed, the deputy mails the fault
// once, records it, and holds it on the next run.
func TestBackupStalenessDeputyMailsWhenThePrimaryIsGone(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	primary.Close()
	cfg := deputyBackupStalenessConfig(t, primary.URL)

	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("with the primary gone the deputy must mail once, sent %d", len(captured.messages))
	}
	if !strings.Contains(logs, "msg=backup.staleness.primary_unreachable") ||
		!strings.Contains(logs, "primary="+primary.URL) {
		t.Fatalf("the unreachable primary must be logged:\n%s", logs)
	}
	if alarmed, found := alarmedBackupMetrics(t, cfg); !found || len(alarmed) != 3 {
		t.Fatalf("the mailed fault must be recorded, found = %v state = %v", found, alarmed)
	}

	runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("a recorded fault must be held, sent %d in total", len(captured.messages))
	}
}

// TestBackupStalenessDeputyMailsWhenThePrimaryDisappearsLater proves a
// deferred fault is not lost: the primary answers on the first run, so the
// deputy stays silent, and is gone on the second, so the deputy mails the
// still-stale fault exactly once.
func TestBackupStalenessDeputyMailsWhenThePrimaryDisappearsLater(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	cfg := deputyBackupStalenessConfig(t, primary.URL)

	runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 0 {
		t.Fatalf("run 1: the primary answered, so the deputy must not mail, sent %d", len(captured.messages))
	}

	primary.Close()
	runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("run 2: the primary is gone, so the deputy must mail once, sent %d", len(captured.messages))
	}
	if alarmed, found := alarmedBackupMetrics(t, cfg); !found || len(alarmed) != 3 {
		t.Fatalf("run 2: the mailed fault must be recorded, found = %v state = %v", found, alarmed)
	}

	runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("run 3: a recorded fault must be held, sent %d in total", len(captured.messages))
	}
}

// TestBackupStalenessDeputyMailsOnANon2xxPrimary treats a primary that answers
// but not with success as absent: the deputy mails and logs the status.
func TestBackupStalenessDeputyMailsOnANon2xxPrimary(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(primary.Close)
	cfg := deputyBackupStalenessConfig(t, primary.URL)

	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("a primary answering 503 is not alive, so the deputy must mail once, sent %d", len(captured.messages))
	}
	if !strings.Contains(logs, "msg=backup.staleness.primary_unreachable") || !strings.Contains(logs, "status=503") {
		t.Fatalf("the primary's status must be logged:\n%s", logs)
	}
}
