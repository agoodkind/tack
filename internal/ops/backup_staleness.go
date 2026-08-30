// backup_staleness.go is the freshness verdict for every backup mechanism:
// how long ago each one last succeeded, whether that age is past the operator's
// threshold, and the report the alert mail carries. Nothing here reads an object
// store, a cluster, or the clock, so the classification the alert acts on is
// exercised from plain values; the probes that supply the timestamps live in
// backup_staleness_check.go.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

const (
	// backupStalenessExportName is the off-host ledger export: the YugabyteDB
	// distributed-snapshot run in the object store.
	backupStalenessExportName = "ledger-export"
	// backupStalenessRehearsalName is the restore drill, the proof the
	// documented recovery procedure still works against real backups.
	backupStalenessRehearsalName = "rehearsal"
	// backupStalenessReplicationName is the live cluster's replication health.
	backupStalenessReplicationName = "replication"
	// backupStalenessFDBName is the FoundationDB continuous backup's
	// restorable point.
	backupStalenessFDBName = "fdb-restorable"
)

// backupStalenessFutureTolerance is how far ahead of the checking host's clock
// a success timestamp may sit and still be read as a real success. No
// mechanism's timestamp is produced by this host at this moment: markers are
// JSON in the object store, the export run key is a name in the store, and the
// FoundationDB restorable point comes off the cluster's clock. A lead of
// seconds to a few minutes is therefore ordinary skew between hosts, and
// anything past it is a wrong or forged clock.
const backupStalenessFutureTolerance = 5 * time.Minute

// backupStalenessMetric is one mechanism's freshness reading: how long ago it
// last succeeded, the age past which the operator must be told, and what the
// reading came from.
type backupStalenessMetric struct {
	Name      string
	Age       time.Duration
	AgeKnown  bool
	Threshold time.Duration
	Detail    string
}

// stale reports whether the mechanism has missed its window. An unknown age is
// stale: it means nothing recorded a success, and a backup mechanism that has
// never demonstrably succeeded is exactly the silent failure this check exists
// to catch.
func (m backupStalenessMetric) stale() bool {
	if !m.AgeKnown {
		return true
	}
	return m.Age > m.Threshold
}

// verdict is the word the report prints for this metric.
func (m backupStalenessMetric) verdict() string {
	if m.stale() {
		return "STALE"
	}
	return "FRESH"
}

// knownBackupStalenessMetric builds a metric from a success timestamp, unless
// that timestamp is implausibly far in the future. Every datable mechanism
// funnels through here, so none can opt out of the plausibility check: a
// timestamp past the skew tolerance is corruption rather than freshness, and
// the metric becomes age-unknown, which classifies as stale. Trusting it
// instead would floor the age at zero and read FRESH against every threshold
// for as long as that timestamp stands, which is the silent failure this whole
// check exists to eliminate.
func knownBackupStalenessMetric(
	ctx context.Context,
	name string,
	now, at time.Time,
	threshold time.Duration,
	detail string,
) backupStalenessMetric {
	if lead := at.Sub(now); lead > backupStalenessFutureTolerance {
		telemetry.L(ctx).ErrorContext(ctx, "backup.staleness.timestamp_implausible",
			slog.String("metric", name),
			slog.Time("at", at.UTC()),
			slog.Time("now", now.UTC()),
			slog.String("lead", backupStalenessSeconds(lead)),
			slog.String("tolerance", backupStalenessSeconds(backupStalenessFutureTolerance)),
		)
		return unknownBackupStalenessMetric(name, threshold,
			implausibleFutureDetail(at, lead, detail))
	}
	return backupStalenessMetric{
		Name:      name,
		Age:       backupStalenessAge(now, at),
		AgeKnown:  true,
		Threshold: threshold,
		Detail:    detail,
	}
}

// implausibleFutureDetail is the report's reason for refusing a success
// timestamp. It keeps the reading's provenance, because that names the marker
// or run whose clock is wrong.
func implausibleFutureDetail(at time.Time, lead time.Duration, provenance string) string {
	detail := fmt.Sprintf("success timestamp %s is %s in the future, past the %s clock-skew tolerance",
		at.UTC().Format(time.RFC3339),
		backupStalenessSeconds(lead),
		backupStalenessSeconds(backupStalenessFutureTolerance))
	if provenance == "" {
		return detail
	}
	return detail + ": " + provenance
}

// unknownBackupStalenessMetric builds a metric for a mechanism with no datable
// success, which classifies as stale. detail carries why the age is unknown,
// because that reason is what the operator acts on.
func unknownBackupStalenessMetric(name string, threshold time.Duration, detail string) backupStalenessMetric {
	return backupStalenessMetric{
		Name:      name,
		Age:       0,
		AgeKnown:  false,
		Threshold: threshold,
		Detail:    detail,
	}
}

// backupStalenessAge is how old a success timestamp is at now, floored at zero
// so the ordinary clock skew knownBackupStalenessMetric tolerates reads as
// brand new rather than as a negative age. A larger lead never reaches here:
// that funnel refuses it before any age is taken.
func backupStalenessAge(now, at time.Time) time.Duration {
	age := now.Sub(at)
	if age < 0 {
		return 0
	}
	return age
}

// backupStalenessThreshold converts a configured threshold in seconds to a
// duration.
func backupStalenessThreshold(seconds int) time.Duration {
	return time.Duration(seconds) * time.Second
}

// backupStalenessReport renders one line per metric, naming the mechanism, its
// age, its threshold, and the verdict, with the reading's provenance last. The
// alert mail carries this text verbatim, so it stays plain and greppable.
func backupStalenessReport(metrics []backupStalenessMetric) string {
	width := 0
	for _, metric := range metrics {
		if len(metric.Name) > width {
			width = len(metric.Name)
		}
	}
	var report strings.Builder
	for _, metric := range metrics {
		fmt.Fprintf(&report, "%-*s age=%s threshold=%s %s",
			width, metric.Name,
			backupStalenessAgeField(metric),
			backupStalenessSeconds(metric.Threshold),
			metric.verdict())
		if detail := backupStalenessDetailField(metric); detail != "" {
			report.WriteString(" " + detail)
		}
		report.WriteString("\n")
	}
	return report.String()
}

// backupStalenessAgeField renders a metric's age, or "unknown" when nothing
// dates the mechanism's last success.
func backupStalenessAgeField(metric backupStalenessMetric) string {
	if !metric.AgeKnown {
		return "unknown"
	}
	return backupStalenessSeconds(metric.Age)
}

// backupStalenessDetailField flattens a metric's provenance onto one line. A
// detail can carry an error string from a store or a cluster, and one line per
// mechanism is the shape the alert mail is read in.
func backupStalenessDetailField(metric backupStalenessMetric) string {
	return strings.Join(strings.Fields(metric.Detail), " ")
}

// backupStalenessSeconds renders a duration as whole seconds, the unit the
// thresholds are configured in.
func backupStalenessSeconds(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10) + "s"
}

// staleBackupStalenessMetrics names every metric past its threshold, in report
// order, for the error the command exits with.
func staleBackupStalenessMetrics(metrics []backupStalenessMetric) []string {
	var stale []string
	for _, metric := range metrics {
		if metric.stale() {
			stale = append(stale, metric.Name)
		}
	}
	return stale
}
