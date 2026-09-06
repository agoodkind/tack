package ops

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// primaryService and deputyService are the --operator-service names the two
// checkers record under.
const (
	primaryService = "tack-backup"
	deputyService  = "tack-backup-deputy"
)

// deputyBackupStalenessConfig is an unreachable-store host that defers to the
// primary checker recorded under primaryService, over the default window.
func deputyBackupStalenessConfig(t *testing.T, primary string) *config.Config {
	t.Helper()
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")
	cfg.BackupAlarmPrimaryService = primary
	cfg.BackupAlarmPrimaryWindowSeconds = 1500
	return cfg
}

// systemLedger is a restored-ledger fixture holding the system org's operator
// events, handed to the deputy in place of the database for the test's
// duration. The query the deputy runs is the real one; only the pool is
// replaced.
type systemLedger struct{ *restoredLedger }

func (systemLedger) Close() {}

// installSystemLedger builds the fixture from rows, makes the deputy open it,
// and returns it so a test can count the rows the deputy read.
func installSystemLedger(t *testing.T, rows ...audit.Row) *restoredLedger {
	t.Helper()
	ledger := &restoredLedger{
		rowsByOrg: map[uuid.UUID][]audit.Row{audit.SystemOrgID(): rows},
		served:    map[uuid.UUID]int{},
	}
	previous := backupAlarmLedgerOpenFunc
	backupAlarmLedgerOpenFunc = func(context.Context, *config.Config) (backupAlarmLedger, error) {
		return systemLedger{ledger}, nil
	}
	t.Cleanup(func() { backupAlarmLedgerOpenFunc = previous })
	return ledger
}

// operatorRunRow is one operator event as the choke-point records it: the
// system org, the service's derived actor id, and the command's verb.
func operatorRunRow(service string, action audit.Verb, at time.Time) audit.Row {
	return audit.Row{
		OrgID:      audit.SystemOrgID(),
		EventTime:  at,
		EventID:    uuid.Must(uuid.NewV7()),
		Seq:        1,
		Shard:      0,
		ActorID:    cli.ServiceActorID(service),
		ActorKind:  1,
		Action:     string(action),
		Outcome:    audit.OutcomeOK,
		EntityKind: "system", EntityID: audit.SystemOrgID(),
		Context:        audit.EventContext{OrgID: audit.SystemOrgID(), Source: audit.SourceSystem},
		Delta:          nil,
		Error:          nil,
		Extra:          nil,
		PIIRef:         nil,
		PrevHash:       nil,
		RowHash:        nil,
		HashVersion:    3,
		IdempotencyKey: "",
	}
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

// TestBackupStalenessDeputyDefersWhileThePrimaryRuns is the one-mail rule
// across guests: with a primary staleness-check event inside the window, the
// deputy leaves a new fault unmailed and unrecorded, on the first run and on
// every run after it.
func TestBackupStalenessDeputyDefersWhileThePrimaryRuns(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	installSystemLedger(t,
		operatorRunRow(primaryService, audit.VerbOpsBackupStalenessCheck, now.Add(-7*time.Minute)))
	cfg := deputyBackupStalenessConfig(t, primaryService)

	for run := 1; run <= 2; run++ {
		logs := runStaleDeputyCheck(t, cfg)
		if len(captured.messages) != 0 {
			t.Fatalf("run %d: a deputy whose primary ran must not mail, sent %d", run, len(captured.messages))
		}
		if _, found := alarmedBackupMetrics(t, cfg); found {
			t.Fatalf("run %d: a deferred fault must not be recorded", run)
		}
		if !strings.Contains(logs, "msg=backup.staleness.alarm_deferred") ||
			!strings.Contains(logs, backupStalenessExportName) ||
			!strings.Contains(logs, "primary="+primaryService) ||
			!strings.Contains(logs, "primary_seen_at=2026-09-05T03:53:00") {
			t.Fatalf("run %d: the deferral must be logged with the metrics, the primary, and its run:\n%s", run, logs)
		}
	}
}

// TestBackupStalenessDeputyMailsWhenThePrimaryHasNotRun covers the deputy's
// reason to exist. The ledger holds the deputy's own run and a different
// primary command inside the window, neither of which is the primary's check,
// so the deputy mails once, records the fault, and holds it on the next run.
func TestBackupStalenessDeputyMailsWhenThePrimaryHasNotRun(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	installSystemLedger(t,
		operatorRunRow(deputyService, audit.VerbOpsBackupStalenessCheck, now.Add(-time.Minute)),
		operatorRunRow(primaryService, audit.VerbOpsBackupRestoreDrill, now.Add(-time.Minute)))
	cfg := deputyBackupStalenessConfig(t, primaryService)

	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("with no primary run in the window the deputy must mail once, sent %d", len(captured.messages))
	}
	if !strings.Contains(logs, "msg=backup.staleness.primary_not_seen") ||
		!strings.Contains(logs, "primary="+primaryService) ||
		!strings.Contains(logs, "window=25m0s") {
		t.Fatalf("the missing primary must be logged with the service and the window:\n%s", logs)
	}
	if alarmed, found := alarmedBackupMetrics(t, cfg); !found || len(alarmed) != 3 {
		t.Fatalf("the mailed fault must be recorded, found = %v state = %v", found, alarmed)
	}

	runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("a recorded fault must be held, sent %d in total", len(captured.messages))
	}
}

// TestBackupStalenessDeputyMailsWhenThePrimaryRanTooLongAgo treats a primary
// run older than the window as no run: the primary has missed more than two
// timer firings, so the deputy mails.
func TestBackupStalenessDeputyMailsWhenThePrimaryRanTooLongAgo(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	installSystemLedger(t,
		operatorRunRow(primaryService, audit.VerbOpsBackupStalenessCheck, now.Add(-26*time.Minute)))
	cfg := deputyBackupStalenessConfig(t, primaryService)

	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("a primary run outside the window must not defer, sent %d", len(captured.messages))
	}
	if !strings.Contains(logs, "msg=backup.staleness.primary_not_seen") {
		t.Fatalf("the missing primary must be logged:\n%s", logs)
	}
}

// TestBackupStalenessDeputyMailsWhenTheLedgerCannotBeRead drives the real
// reader at a port that refuses connections: a deputy that cannot read the
// ledger mails, and says why.
func TestBackupStalenessDeputyMailsWhenTheLedgerCannotBeRead(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := deputyBackupStalenessConfig(t, primaryService)
	cfg.AuditReaderDSN = "postgres://tack_audit_reader:unused@127.0.0.1:1/tack?sslmode=disable" // gitleaks:allow test placeholder

	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("a deputy that cannot read the ledger must mail once, sent %d", len(captured.messages))
	}
	if !strings.Contains(logs, "msg=backup.staleness.primary_not_seen") ||
		!strings.Contains(logs, "err=") ||
		!strings.Contains(logs, "read the primary's runs from the ledger") {
		t.Fatalf("the unreadable ledger must be logged with its error:\n%s", logs)
	}
	if alarmed, found := alarmedBackupMetrics(t, cfg); !found || len(alarmed) != 3 {
		t.Fatalf("the mailed fault must be recorded, found = %v state = %v", found, alarmed)
	}
}

// TestBackupStalenessPrimaryIgnoresTheLedger pins that a checker with no
// primary service is the primary: it mails whatever the ledger says, and reads
// none of it.
func TestBackupStalenessPrimaryIgnoresTheLedger(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	ledger := installSystemLedger(t,
		operatorRunRow(primaryService, audit.VerbOpsBackupStalenessCheck, now.Add(-time.Minute)))
	cfg := deputyBackupStalenessConfig(t, "")

	runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("the primary must mail once, sent %d", len(captured.messages))
	}
	if ledger.served[audit.SystemOrgID()] != 0 {
		t.Fatalf("the primary must not read the ledger, read %d rows", ledger.served[audit.SystemOrgID()])
	}
}
