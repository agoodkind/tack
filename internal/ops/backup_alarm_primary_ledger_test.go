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

// primaryService and deputyService identify the checkers' --operator-service
// values for recorded events.
const (
	primaryService = "tack-backup"
	deputyService  = "tack-backup-deputy"
)

// deputyBackupStalenessConfig configures an unreachable-store deputy with
// primary and the default window and grace. The replacement wait returns at
// once and counts calls. A test can replace it again to change ledger state
// during grace without sleeping.
func deputyBackupStalenessConfig(t *testing.T, primary string) *config.Config {
	t.Helper()
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")
	cfg.BackupAlarmPrimaryService = primary
	cfg.BackupAlarmPrimaryWindowSeconds = 1500
	cfg.BackupAlarmPrimaryGraceSeconds = 90
	skipBackupAlarmGrace(t, func() {})

	return cfg
}

// skipBackupAlarmGrace runs during in place of the grace wait and returns a
// counter for the deputy's wait requests.
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

// blockingLedger waits for ctx to end before returning a read error,
// representing a pool blocked by a database partition.
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

// systemLedger supplies restored operator rows for the system organization
// during the test. It replaces the reader, while the deputy still builds the
// production query.
type systemLedger struct{ *restoredLedger }

func (systemLedger) Close() {}

// installSystemLedger builds a fixture from rows and replaces the deputy's
// ledger opener. The returned fixture counts the rows that the deputy reads.
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

// operatorRunRow constructs an operator event with the system organization,
// the service's derived actor ID, and the command verb used by the audit
// boundary.
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

// runStaleDeputyCheck runs the command with a context logger, requires the
// staleness error, and returns the captured log text.
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

// TestBackupStalenessDeputyDefersWhileThePrimaryRuns checks repeated deputy
// runs with a primary staleness-check event inside the window. Each run must
// defer the new fault without mailing or recording it.
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
				"run %d: a deputy whose primary ran must not mail, sent %d",
				run,
				len(captured.messages),
			)
		}

		if _, found := alarmedBackupMetrics(t, cfg); found {
			t.Fatalf("run %d: a deferred fault must not be recorded", run)
		}

		if !strings.Contains(logs, "msg=backup.staleness.alarm_deferred") ||
			!strings.Contains(logs, backupStalenessExportName) ||
			!strings.Contains(logs, "primary="+primaryService) ||
			!strings.Contains(logs, "primary_seen_at=2026-09-05T03:53:00") {
			t.Fatalf(
				"run %d: the deferral must be logged with the metrics, the primary, and its run:\n%s",
				run,
				logs,
			)
		}
	}
}

// TestBackupStalenessDeputyMailsWhenThePrimaryHasNotRun supplies a deputy's
// own check and a different primary command inside the window. Neither event
// qualifies as the primary's staleness check. The deputy must mail once, record
// acceptance, and suppress another message on its next run.
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
			"with no primary run in the window the deputy must mail once, sent %d",
			len(captured.messages),
		)
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

// TestBackupStalenessDeputyMailsWhenThePrimaryRanTooLongAgo puts the primary
// run outside the window. The original regression account relates that age to
// more than two missed timer firings. The deputy must mail without deferring
// to the old run.
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
		t.Fatalf("a primary run outside the window must not defer, sent %d", len(captured.messages))
	}

	if !strings.Contains(logs, "msg=backup.staleness.primary_not_seen") {
		t.Fatalf("the missing primary must be logged:\n%s", logs)
	}
}

// TestBackupStalenessDeputyMailsWhenTheLedgerCannotBeRead uses the production
// reader with a port that refuses connections. The deputy must mail and log
// the reason that it could not read the ledger.
func TestBackupStalenessDeputyMailsWhenTheLedgerCannotBeRead(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := deputyBackupStalenessConfig(t, primaryService)
	cfg.AuditReaderDSN = "postgres://tack_audit_reader:unused@127.0.0.1:1/tack?sslmode=disable" // gitleaks:allow test placeholder

	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"a deputy that cannot read the ledger must mail once, sent %d",
			len(captured.messages),
		)
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

// TestBackupStalenessDeputyMailsWhenTheLedgerHangs uses a blocking read. The
// query timeout must end the read, and the deputy must treat it as unreadable
// evidence and mail the fault.
func TestBackupStalenessDeputyMailsWhenTheLedgerHangs(t *testing.T) {
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
			"a deputy whose ledger read timed out must mail once, sent %d",
			len(captured.messages),
		)
	}

	if ledger.blockedFor == 0 || ledger.blockedFor > time.Second {
		t.Fatalf(
			"the read must be cut off at the query timeout, it blocked for %s",
			ledger.blockedFor,
		)
	}

	if !strings.Contains(logs, "msg=backup.staleness.primary_not_seen") ||
		!strings.Contains(logs, "context deadline exceeded") {
		t.Fatalf("the timed-out read must be logged as an unreadable ledger:\n%s", logs)
	}
}

// TestBackupStalenessDeputyDefersWhenThePrimaryRunLandsDuringGrace adds the
// primary row after the first read and before the second. The deputy must wait
// for grace, then defer without mailing or recording the fault.
func TestBackupStalenessDeputyDefersWhenThePrimaryRunLandsDuringGrace(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	ledger := installSystemLedger(t)
	cfg := deputyBackupStalenessConfig(t, primaryService)
	// The fixed clock dates the inserted row one second before the second read,
	// matching a commit that precedes the read that observes it.
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
			"a primary run that landed during the grace period must defer, sent %d",
			len(captured.messages),
		)
	}

	if _, found := alarmedBackupMetrics(t, cfg); found {
		t.Fatal("a deferred fault must not be recorded")
	}

	if *waits != 1 {
		t.Fatalf("the deputy must wait out the grace period once, waited %d times", *waits)
	}

	if !strings.Contains(logs, "msg=backup.staleness.primary_grace") ||
		!strings.Contains(logs, "seconds=90") ||
		!strings.Contains(logs, "msg=backup.staleness.alarm_deferred") {
		t.Fatalf("the grace wait and the deferral must both be logged:\n%s", logs)
	}
}

// TestBackupStalenessDeputyMailsAfterGraceFindsNoPrimaryRun omits a primary
// event from both reads. The deputy must wait once, then mail once.
func TestBackupStalenessDeputyMailsAfterGraceFindsNoPrimaryRun(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	installSystemLedger(t)
	cfg := deputyBackupStalenessConfig(t, primaryService)
	waits := skipBackupAlarmGrace(t, func() {})

	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"with no primary run after the grace period the deputy must mail once, sent %d",
			len(captured.messages),
		)
	}

	if *waits != 1 {
		t.Fatalf("the deputy must wait out the grace period once, waited %d times", *waits)
	}

	if !strings.Contains(logs, "msg=backup.staleness.primary_grace") ||
		!strings.Contains(logs, "msg=backup.staleness.primary_not_seen") {
		t.Fatalf("the grace wait and the missing primary must both be logged:\n%s", logs)
	}
}

// TestBackupStalenessPrimaryIgnoresTheLedger leaves the primary service
// unset. The checker acts as primary, mails its fault, and does not read ledger
// events even when the fixture contains a recent primary run.
func TestBackupStalenessPrimaryIgnoresTheLedger(t *testing.T) {
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
		t.Fatalf("the primary must mail once, sent %d", len(captured.messages))
	}

	if ledger.served[audit.SystemOrgID()] != 0 {
		t.Fatalf(
			"the primary must not read the ledger, read %d rows",
			ledger.served[audit.SystemOrgID()],
		)
	}
}
