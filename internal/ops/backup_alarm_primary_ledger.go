// backup_alarm_primary_ledger.go keeps one fault to one mail when the
// staleness check runs on more than one guest and the object store is down.
// The guests share an alarm memory in the store (backup_alarm_memory.go), but
// the fault that first produced duplicate mails was the store refusing writes
// (TACK-481), so that memory fails exactly when the alarm is needed. For that
// case one checker is the primary and the others are deputies, and the proof
// that the primary is doing its job is the audit ledger: every ops command
// records an operator event through the clispec choke-point, the ledger is
// quorum-replicated across the data guests, and it stays readable when the
// object store is down. A deputy defers a new fault while the ledger holds a
// recent staleness-check event recorded by the primary's service actor, and
// leaves the fault unrecorded, so it mails from the deputy on a later run if
// the primary stops running while the fault is still stale.
//
// Two bounds keep the check honest. Each ledger read is given a fixed time,
// and a read that runs out of it counts as a ledger that cannot be read, so a
// hung pool never holds the alarm. And a first read that finds no fresh
// primary run is not yet a takeover: after a fresh deployment or a long pause
// both timers fire within seconds of each other, and the primary's intent row
// may not be committed when the deputy looks, so the deputy waits out a grace
// period and reads once more before it mails.

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

// backupAlarmLedger is what the deputy reads the primary's runs from: the
// ledger reader in production, or a row source standing in for it in a test.
type backupAlarmLedger interface {
	audit.RowSource
	Close()
}

// backupAlarmLedgerOpenFunc opens the ledger. It is a package variable for
// the same reason backupAlarmSendFunc is: a test hands the deputy a ledger
// fixture in place of a database, while the query it runs stays the real one.
var backupAlarmLedgerOpenFunc = openBackupAlarmLedger

// backupAlarmPrimaryQueryTimeout bounds one ledger read, pool open and query
// together. It is a variable so a test can shorten it.
var backupAlarmPrimaryQueryTimeout = 10 * time.Second

// backupAlarmSleepFunc waits out the grace period. It is a package variable
// for the same reason nowFunc is: a test substitutes it so the wait costs
// nothing and so it can change the ledger between the two reads.
var backupAlarmSleepFunc = sleepBackupAlarmGrace

// sleepBackupAlarmGrace waits for the grace period or the context, whichever
// ends first.
func sleepBackupAlarmGrace(ctx context.Context, grace time.Duration) {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// openBackupAlarmLedger opens the audit reader pool the deputy's container
// carries as AUDIT_READER_DSN. The transport detail is logged here and the
// alarm-level verdict at the caller, the way the mailer logs a transport
// failure under the alarm's own verdict line.
func openBackupAlarmLedger(ctx context.Context, cfg *config.Config) (backupAlarmLedger, error) {
	reader, err := audit.NewReader(ctx, cfg.AuditReaderDSN)
	if err != nil {
		wrapped := fmt.Errorf("open the ledger reader: %w", err)
		telemetry.L(ctx).WarnContext(ctx, "backup.staleness.ledger_open_failed",
			slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	return reader, nil
}

// backupAlarmDeferredToPrimary reports whether a deputy should leave these
// faults to the primary. A checker with no primary service is the primary and
// never defers. A deputy defers only when the ledger holds a staleness-check
// event recorded by the primary's service within the window, on the first
// read or on the one after the grace period; no such event, or a ledger that
// cannot be read, means the deputy mails as the primary would.
func backupAlarmDeferredToPrimary(ctx context.Context, cfg *config.Config, faults []backupStalenessMetric) bool {
	primary := cfg.BackupAlarmPrimaryService
	if primary == "" {
		return false
	}
	logger := telemetry.L(ctx)
	window := time.Duration(cfg.BackupAlarmPrimaryWindowSeconds) * time.Second
	seenAt, seen, err := backupAlarmPrimaryRunInWindow(ctx, cfg, primary, window)
	if err == nil && !seen {
		logger.InfoContext(ctx, "backup.staleness.primary_grace",
			slog.String("primary", primary), slog.Int("seconds", cfg.BackupAlarmPrimaryGraceSeconds))
		backupAlarmSleepFunc(ctx, time.Duration(cfg.BackupAlarmPrimaryGraceSeconds)*time.Second)
		seenAt, seen, err = backupAlarmPrimaryRunInWindow(ctx, cfg, primary, window)
	}
	if err != nil {
		logger.WarnContext(ctx, "backup.staleness.primary_not_seen",
			slog.String("primary", primary), slog.String("window", window.String()),
			slog.String("err", err.Error()))
		return false
	}
	if !seen {
		logger.WarnContext(ctx, "backup.staleness.primary_not_seen",
			slog.String("primary", primary), slog.String("window", window.String()))
		return false
	}
	names := make([]string, 0, len(faults))
	for _, fault := range faults {
		names = append(names, fault.Name)
	}
	logger.InfoContext(ctx, "backup.staleness.alarm_deferred",
		slog.Any("metrics", names), slog.String("primary", primary),
		slog.Time("primary_seen_at", seenAt))
	return true
}

// backupAlarmPrimaryRunInWindow is one bounded ledger read: the window ends
// at this instant, and the whole read, pool open included, has the query
// timeout to finish in.
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

// newestBackupAlarmPrimaryRun reads the newest staleness-check event the
// primary's service actor recorded in [oldest, latest). The actor filter is
// what separates the primary's runs from this deputy's own, which record the
// same verb under the deputy's service. The org is the system org every
// operator event carries. seen is false when the window holds no such event.
func newestBackupAlarmPrimaryRun(
	ctx context.Context,
	cfg *config.Config,
	primary string,
	oldest, latest time.Time,
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
	err = ledger.StreamQuery(ctx, filter, func(row audit.Row) error {
		if !seen || row.EventTime.After(seenAt) {
			seenAt = row.EventTime
			seen = true
		}
		return nil
	})
	if err != nil {
		wrapped := fmt.Errorf("read the primary's runs from the ledger: %w", err)
		telemetry.L(ctx).WarnContext(ctx, "backup.staleness.ledger_read_failed",
			slog.String("err", wrapped.Error()))
		return time.Time{}, false, wrapped
	}
	return seenAt, seen, nil
}
