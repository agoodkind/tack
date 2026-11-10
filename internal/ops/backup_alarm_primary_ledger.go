// Primary and deputy checkers limit duplicate alarm mail when shared object
// memory cannot record a fault. TACK-481 documented duplicate messages after
// the store refused writes. Every ops command must record an operator event
// through the clispec audit boundary. The quorum-replicated ledger on the data
// guests provides a separate source of primary-run evidence during a store
// outage, subject to a successful ledger read.
//
// A recent staleness-check event from the primary's service actor defers a
// deputy's new fault without recording it. If the primary stops and the fault
// remains stale, a later deputy run can mail it. Each read uses a timeout.
// A timeout counts as unreadable evidence and does not defer mail.
//
// After deployment or a long pause, the primary and deputy timers can run
// seconds apart before the primary intent row commits. A successful first read
// without a recent primary event triggers a grace wait and one more read
// before the deputy decides whether to mail.

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

// backupAlarmLedger supplies primary-run rows through the audit reader in
// production or a replacement row source in tests.
type backupAlarmLedger interface {
	audit.RowSource
	Close()
}

// backupAlarmLedgerOpenFunc opens the ledger. Tests replace this package
// variable, like backupAlarmSendFunc, with a fixture. The deputy still builds
// the production query.
var backupAlarmLedgerOpenFunc = openBackupAlarmLedger

// backupAlarmPrimaryQueryTimeout limits pool opening and querying together
// for one ledger read. Tests can shorten this variable.
var backupAlarmPrimaryQueryTimeout = 10 * time.Second

// backupAlarmSleepFunc waits for grace. Tests replace this variable, like
// nowFunc, to skip elapsed waiting and change ledger rows between reads.
var backupAlarmSleepFunc = sleepBackupAlarmGrace

// sleepBackupAlarmGrace waits until the grace timer expires or ctx ends.
func sleepBackupAlarmGrace(ctx context.Context, grace time.Duration) {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// openBackupAlarmLedger opens the reader with AUDIT_READER_DSN from the
// deputy's configuration. It logs connection failures here; the caller logs
// the alarm decision, as the mailer separates transport and alarm results.
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

// backupAlarmDeferredToPrimary reports whether the deputy must defer these
// faults. A checker without a primary service acts as primary and does not
// defer. A deputy defers when either read finds a staleness-check event from
// the primary service inside the window. An empty successful first read waits
// for grace before the second read. Absent or unreadable evidence permits mail.
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

// backupAlarmPrimaryRunInWindow reads the window ending at the current UTC
// instant. One query timeout includes pool opening and the query.
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

// newestBackupAlarmPrimaryRun reads the newest staleness-check event from
// the primary service actor in [oldest, latest). The actor filter excludes
// deputy events with the same verb. The query uses the system organization for
// operator events. seen is false when the window does not contain a match.
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
