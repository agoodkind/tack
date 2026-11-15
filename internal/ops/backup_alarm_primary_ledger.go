// Deputies use recent primary audit events to suppress duplicate fault email
// when the shared object store cannot persist alarm state (TACK-481). Operator
// events use the replicated ledger independently of object-store availability.
// Deferral does not record acceptance; a later deputy can email if primary
// activity stops. Bounded reads prevent ledger failure from blocking alarms.
// A grace interval and second read allow a concurrent primary event to commit.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// backupAlarmLedger supplies operator rows with a closeable production reader.
type backupAlarmLedger interface {
	audit.RowSource
	Close()
}

// backupAlarmLedgerOpenFunc permits fixture pools while retaining the
// production query in tests.
var backupAlarmLedgerOpenFunc = openBackupAlarmLedger

// backupAlarmPrimaryQueryTimeout bounds pool opening and querying together.
// Tests can shorten this variable.
var backupAlarmPrimaryQueryTimeout = 10 * time.Second

// backupAlarmSleepFunc permits grace callbacks without real-time test delays.
var backupAlarmSleepFunc = sleepBackupAlarmGrace

// sleepBackupAlarmGrace waits until grace expires or context ends.
func sleepBackupAlarmGrace(ctx context.Context, grace time.Duration) {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// openBackupAlarmLedger opens AUDIT_READER_DSN and logs transport failures.
// The caller separately records the resulting alarm decision.
func openBackupAlarmLedger(ctx context.Context, cfg *config.Config) (backupAlarmLedger, error) {
	reader, err := audit.NewReader(ctx, cfg.AuditReaderDSN)
	if err != nil {
		wrapped := fmt.Errorf("open the ledger reader from AUDIT_READER_DSN: %w", err)
		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.staleness.ledger_open_failed",
			slog.String("err", wrapped.Error()),
		)

		return nil, wrapped
	}

	return reader, nil
}

// backupAlarmDeferredToPrimary requires a recent primary staleness-check event
// on the initial read or after grace. Unset primary service selects primary
// behavior without ledger reads. Missing evidence or unreadable ledger permits
// deputy email instead of silent deferral.
func backupAlarmDeferredToPrimary(
	ctx context.Context,
	cfg *config.Config,
	faults []backupStalenessMetric,
) bool {
	primary := cfg.BackupAlarmPrimaryService
	if primary == "" {
		return false
	}

	logger := telemetry.L(ctx)
	window := time.Duration(cfg.BackupAlarmPrimaryWindowSeconds) * time.Second
	seenAt, seen, err := backupAlarmPrimaryRunInWindow(ctx, cfg, primary, window)
	if err == nil && !seen {
		logger.InfoContext(
			ctx,
			"backup.staleness.primary_grace",
			slog.String("primary", primary),
			slog.Int("seconds", cfg.BackupAlarmPrimaryGraceSeconds),
		)
		backupAlarmSleepFunc(ctx, time.Duration(cfg.BackupAlarmPrimaryGraceSeconds)*time.Second)
		seenAt, seen, err = backupAlarmPrimaryRunInWindow(ctx, cfg, primary, window)
	}

	if err != nil {
		logger.WarnContext(
			ctx,
			"backup.staleness.primary_not_seen",
			slog.String("primary", primary),
			slog.String("window", window.String()),
			slog.String("err", err.Error()),
		)

		return false
	}

	if !seen {
		logger.WarnContext(
			ctx,
			"backup.staleness.primary_not_seen",
			slog.String("primary", primary),
			slog.String("window", window.String()),
		)

		return false
	}

	names := make([]string, 0, len(faults))
	for _, fault := range faults {
		names = append(names, fault.Name)
	}

	logger.InfoContext(
		ctx,
		"backup.staleness.alarm_deferred",
		slog.Any("metrics", names),
		slog.String("primary", primary),
		slog.Time("primary_seen_at", seenAt),
	)

	return true
}

// backupAlarmPrimaryRunInWindow reads recent activity with one deadline for
// pool opening and query execution. The selection window ends at current time.
func backupAlarmPrimaryRunInWindow(
	ctx context.Context,
	cfg *config.Config,
	primary string,
	window time.Duration,
) (seenAt time.Time, seen bool, err error) {
	queryCtx, cancel := context.WithTimeout(ctx, backupAlarmPrimaryQueryTimeout)
	defer cancel()
	latest := opsNow().UTC()

	return newestBackupAlarmPrimaryRun(queryCtx, cfg, primary, latest.Add(-window), latest)
}

// newestBackupAlarmPrimaryRun selects staleness-check events for the primary's
// service actor in [oldest, latest). System-org and actor filters exclude
// unrelated commands and the deputy's own events.
// Missing events return seen=false.
func newestBackupAlarmPrimaryRun(
	ctx context.Context,
	cfg *config.Config,
	primary string,
	oldest time.Time,
	latest time.Time,
) (seenAt time.Time, seen bool, err error) {
	ledger, err := backupAlarmLedgerOpenFunc(ctx, cfg)
	if err != nil {
		return time.Time{}, false, err
	}
	defer ledger.Close()
	filter := audit.QueryFilter{
		OrgID:     audit.SystemOrgID(),
		Oldest:    oldest,
		Latest:    latest,
		Action:    string(audit.VerbOpsBackupStalenessCheck),
		ActorID:   cli.ServiceActorID(primary),
		EntityID:  uuid.Nil,
		RequestID: "",
		TraceID:   "",
		Limit:     1,
	}
	err = ledger.StreamQuery(
		ctx,
		filter,
		func(row audit.Row) error {
			if !seen || row.EventTime.After(seenAt) {
				seenAt = row.EventTime
				seen = true
			}

			return nil
		},
	)
	if err != nil {
		wrapped := fmt.Errorf("read the primary's runs from the ledger: %w", err)
		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.staleness.ledger_read_failed",
			slog.String("err", wrapped.Error()),
		)

		return time.Time{}, false, wrapped
	}

	return seenAt, seen, nil
}
