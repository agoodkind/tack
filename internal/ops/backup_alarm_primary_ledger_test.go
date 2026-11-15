package ops

import (
	"bytes"
	"context"
	"fmt"
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

// Service labels match the --operator-service identities recorded by checkers.
const (
	primaryService = "tack-backup"
	deputyService  = "tack-backup-deputy"
)

// deputyBackupStalenessConfig configures refused store and master connections
// with primary deferral. Tests replace grace delays with immediate callbacks.
func deputyBackupStalenessConfig(t *testing.T, primary string) *config.Config {
	t.Helper()
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")
	cfg.BackupAlarmPrimaryService = primary
	cfg.BackupAlarmPrimaryWindowSeconds = 1500
	cfg.BackupAlarmPrimaryGraceSeconds = 90
	skipBackupAlarmGrace(t, func() {})

	return cfg
}

// skipBackupAlarmGrace invokes during instead of waiting and counts requests.
func skipBackupAlarmGrace(t *testing.T, during func()) *int {
	t.Helper()
	waits := 0
	previous := backupAlarmSleepFunc
	backupAlarmSleepFunc = func(context.Context, time.Duration) {
		waits++
		during()
	}
	t.Cleanup(func() { backupAlarmSleepFunc = previous })

	return &waits
}

// blockingLedger simulates a ledger read that ends only after cancellation.
type blockingLedger struct{ blockedFor time.Duration }

func (l *blockingLedger) StreamQuery(
	ctx context.Context,
	_ audit.QueryFilter,
	_ audit.RowVisitor,
) error {
	started := time.Now()
	<-ctx.Done()
	l.blockedFor = time.Since(started)

	return fmt.Errorf("blocked ledger: %w", ctx.Err())
}

func (*blockingLedger) Close() {}

// systemLedger supplies operator rows while exercising the deputy's production
// query with a substituted database pool.
type systemLedger struct{ *restoredLedger }

func (systemLedger) Close() {}

// installSystemLedger supplies the system-org fixture for ledger opening and
// returns its read counters for assertions.
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

// operatorRunRow reproduces the system organization, service actor, and
// operation verb from operator audit recording.
func operatorRunRow(service string, action audit.Verb, at time.Time) audit.Row {
	return audit.Row{
		OrgID:          audit.SystemOrgID(),
		EventTime:      at,
		EventID:        uuid.Must(uuid.NewV7()),
		Seq:            1,
		Shard:          0,
		ActorID:        cli.ServiceActorID(service),
		ActorKind:      1,
		Action:         string(action),
		Outcome:        audit.OutcomeOK,
		EntityKind:     "system",
		EntityID:       audit.SystemOrgID(),
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

// runStaleDeputyCheck verifies the public command's stale verdict and returns
// contextual logger output.
func runStaleDeputyCheck(t *testing.T, cfg *config.Config) string {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(
		slog.NewTextHandler(
			&logs,
			&slog.HandlerOptions{
				AddSource:   false,
				Level:       slog.LevelDebug,
				ReplaceAttr: nil,
			},
		),
	)
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

// TestBackupStalenessDeputyDefersWhileThePrimaryRuns checks repeated deferral
// without email or fault recording while recent primary evidence exists.
func TestBackupStalenessDeputyDefersWhileThePrimaryRuns(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	installSystemLedger(
		t,
		operatorRunRow(primaryService, audit.VerbOpsBackupStalenessCheck, now.Add(-7*time.Minute)),
	)
	cfg := deputyBackupStalenessConfig(t, primaryService)

	for run := 1; run <= 2; run++ {
		logs := runStaleDeputyCheck(t, cfg)
		if len(captured.messages) != 0 {
			t.Fatalf(
				"run %d: recent primary evidence must suppress deputy email, sent %d",
				run,
				len(captured.messages),
			)
		}

		if _, found := alarmedBackupMetrics(t, cfg); found {
			t.Fatalf("run %d: deferral must not record a fault", run)
		}

		if !strings.Contains(logs, "msg=backup.staleness.alarm_deferred") ||
			!strings.Contains(logs, backupStalenessExportName) ||
			!strings.Contains(logs, "primary="+primaryService) ||
			!strings.Contains(logs, "primary_seen_at=2026-09-05T03:53:00") {
			t.Fatalf(
				"run %d: deferral logs must include metrics, primary identity, and its run time:\n%s",
				run,
				logs,
			)
		}
	}
}

// TestBackupStalenessDeputyMailsWhenThePrimaryHasNotRun excludes deputy checks
// and unrelated primary operations from proof of recent primary checking.
func TestBackupStalenessDeputyMailsWhenThePrimaryHasNotRun(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	installSystemLedger(
		t,
		operatorRunRow(deputyService, audit.VerbOpsBackupStalenessCheck, now.Add(-time.Minute)),
		operatorRunRow(primaryService, audit.VerbOpsBackupRestoreDrill, now.Add(-time.Minute)),
	)
	cfg := deputyBackupStalenessConfig(t, primaryService)

	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"the deputy must email once without a recent primary check, sent %d",
			len(captured.messages),
		)
	}

	if !strings.Contains(logs, "msg=backup.staleness.primary_not_seen") ||
		!strings.Contains(logs, "primary="+primaryService) ||
		!strings.Contains(logs, "window=25m0s") {
		t.Fatalf("missing-primary logs must include service and window:\n%s", logs)
	}

	if alarmed, found := alarmedBackupMetrics(t, cfg); !found || len(alarmed) != 3 {
		t.Fatalf("state must record accepted email, found = %v state = %v", found, alarmed)
	}

	runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"recorded faults must suppress duplicate email, sent %d total",
			len(captured.messages),
		)
	}
}

// TestBackupStalenessDeputyMailsWhenThePrimaryRanTooLongAgo checks expiry of
// primary evidence outside the configured activity window.
func TestBackupStalenessDeputyMailsWhenThePrimaryRanTooLongAgo(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	installSystemLedger(
		t,
		operatorRunRow(primaryService, audit.VerbOpsBackupStalenessCheck, now.Add(-26*time.Minute)),
	)
	cfg := deputyBackupStalenessConfig(t, primaryService)

	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"expired primary evidence must permit deputy email, sent %d",
			len(captured.messages),
		)
	}

	if !strings.Contains(logs, "msg=backup.staleness.primary_not_seen") {
		t.Fatalf("logs must report missing recent primary activity:\n%s", logs)
	}
}

// TestBackupStalenessDeputyMailsWhenTheLedgerCannotBeRead checks production
// reader failure against a refused connection and subsequent fault email.
func TestBackupStalenessDeputyMailsWhenTheLedgerCannotBeRead(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := deputyBackupStalenessConfig(t, primaryService)
	cfg.AuditReaderDSN = "postgres://tack_audit_reader:unused@127.0.0.1:1/tack?sslmode=disable" // gitleaks:allow test placeholder

	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"an unreadable ledger must permit one deputy email, sent %d",
			len(captured.messages),
		)
	}

	if !strings.Contains(logs, "msg=backup.staleness.primary_not_seen") ||
		!strings.Contains(logs, "err=") ||
		!strings.Contains(logs, "read the primary's runs from the ledger") {
		t.Fatalf("logs must report unreadable ledger details:\n%s", logs)
	}

	if alarmed, found := alarmedBackupMetrics(t, cfg); !found || len(alarmed) != 3 {
		t.Fatalf("state must record accepted email, found = %v state = %v", found, alarmed)
	}
}

// TestBackupStalenessDeputyEmailsAfterLedgerTimeout checks a bounded unanswered
// query followed by deputy email and an observation-failure log.
func TestBackupStalenessDeputyEmailsAfterLedgerTimeout(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	previousTimeout := backupAlarmPrimaryQueryTimeout
	backupAlarmPrimaryQueryTimeout = 50 * time.Millisecond
	t.Cleanup(func() { backupAlarmPrimaryQueryTimeout = previousTimeout })
	ledger := &blockingLedger{blockedFor: 0}
	previousOpen := backupAlarmLedgerOpenFunc
	backupAlarmLedgerOpenFunc = func(context.Context, *config.Config) (backupAlarmLedger, error) {
		return ledger, nil
	}
	t.Cleanup(func() { backupAlarmLedgerOpenFunc = previousOpen })
	cfg := deputyBackupStalenessConfig(t, primaryService)

	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"a timed-out ledger read must permit one deputy email, sent %d",
			len(captured.messages),
		)
	}

	if ledger.blockedFor == 0 || ledger.blockedFor > time.Second {
		t.Fatalf(
			"query timeout must bound the read, observed duration %s",
			ledger.blockedFor,
		)
	}

	if !strings.Contains(logs, "msg=backup.staleness.primary_not_seen") ||
		!strings.Contains(logs, "context deadline exceeded") {
		t.Fatalf("logs must classify the timed-out read as unreadable:\n%s", logs)
	}
}

// TestBackupStalenessDeputyDefersAfterPrimaryCommitsDuringGrace checks the
// second ledger read after a primary event appears during the grace interval.
func TestBackupStalenessDeputyDefersAfterPrimaryCommitsDuringGrace(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	ledger := installSystemLedger(t)
	cfg := deputyBackupStalenessConfig(t, primaryService)
	// Fixed test time places the event one second before the second read.
	waits := skipBackupAlarmGrace(
		t,
		func() {
			ledger.rowsByOrg[audit.SystemOrgID()] = append(
				ledger.rowsByOrg[audit.SystemOrgID()],
				operatorRunRow(
					primaryService,
					audit.VerbOpsBackupStalenessCheck,
					now.Add(-time.Second),
				),
			)
		},
	)

	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 0 {
		t.Fatalf(
			"a primary commit during grace must defer deputy email, sent %d",
			len(captured.messages),
		)
	}

	if _, found := alarmedBackupMetrics(t, cfg); found {
		t.Fatal("deferral must not record a fault")
	}

	if *waits != 1 {
		t.Fatalf("grace waits = %d, want 1", *waits)
	}

	if !strings.Contains(logs, "msg=backup.staleness.primary_grace") ||
		!strings.Contains(logs, "seconds=90") ||
		!strings.Contains(logs, "msg=backup.staleness.alarm_deferred") {
		t.Fatalf("logs must report the grace wait and deferral:\n%s", logs)
	}
}

// TestBackupStalenessDeputyEmailsWhenPrimaryAbsentAfterGrace checks one grace
// wait and one email after both ledger reads fail to find recent primary work.
func TestBackupStalenessDeputyEmailsWhenPrimaryAbsentAfterGrace(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	installSystemLedger(t)
	cfg := deputyBackupStalenessConfig(t, primaryService)
	waits := skipBackupAlarmGrace(t, func() {})

	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"the deputy must email once when primary work remains absent after grace, sent %d",
			len(captured.messages),
		)
	}

	if *waits != 1 {
		t.Fatalf("grace waits = %d, want 1", *waits)
	}

	if !strings.Contains(logs, "msg=backup.staleness.primary_grace") ||
		!strings.Contains(logs, "msg=backup.staleness.primary_not_seen") {
		t.Fatalf("logs must report grace and absent primary work:\n%s", logs)
	}
}

// TestBackupStalenessPrimaryEmailsWithoutLedgerRead checks that an unset
// primary service selects primary behavior independently of ledger evidence.
func TestBackupStalenessPrimaryEmailsWithoutLedgerRead(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	ledger := installSystemLedger(
		t,
		operatorRunRow(primaryService, audit.VerbOpsBackupStalenessCheck, now.Add(-time.Minute)),
	)
	cfg := deputyBackupStalenessConfig(t, "")

	runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("the primary must email once, sent %d", len(captured.messages))
	}

	if ledger.served[audit.SystemOrgID()] != 0 {
		t.Fatalf(
			"the primary must not read ledger rows, read %d",
			ledger.served[audit.SystemOrgID()],
		)
	}
}
