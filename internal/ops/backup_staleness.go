// Backup staleness classification compares success ages against configured
// thresholds and formats readings. Probes supply timestamps; these functions
// do not read the object store, cluster, or clock.

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
	// backupStalenessExportName identifies off-host distributed SQL exports.
	backupStalenessExportName = "ledger-export"
	// backupStalenessRehearsalName identifies restore drill success.
	backupStalenessRehearsalName = "rehearsal"
	// backupStalenessReplicationName identifies live replication health.
	backupStalenessReplicationName = "replication"
	// backupStalenessFDBName identifies the continuous backup's restorable point.
	backupStalenessFDBName = "fdb-restorable"
)

// backupStalenessFutureTolerance permits small clock differences between
// checking hosts, object-store records, and database timestamps. Larger future
// timestamps cannot establish recent success and may indicate corruption.
const backupStalenessFutureTolerance = 5 * time.Minute

// backupStalenessMetric records freshness and its evidence. At records the
// last success in UTC or zero for unknown age. Unknown records the failure
// cause even when a historical reading supplies a known age. LastReadAt dates
// that historical observation.
type backupStalenessMetric struct {
	Name       string
	At         time.Time
	Age        time.Duration
	AgeKnown   bool
	Unknown    backupStalenessUnknownCause
	Threshold  time.Duration
	Detail     string
	LastReadAt time.Time `exhaustruct:"optional"`
}

// stale reports an unknown age or an age above the threshold. Unknown ages
// cannot demonstrate a recent success.
func (m backupStalenessMetric) stale() bool {
	if !m.AgeKnown {
		return true
	}

	return m.Age > m.Threshold
}

// verdict returns the metric's report label.
func (m backupStalenessMetric) verdict() string {
	if m.stale() {
		return "STALE"
	}

	return "FRESH"
}

// knownBackupStalenessMetric validates every datable success timestamp against
// future skew before calculating age. Timestamps beyond tolerance become
// unreadable and stale instead of remaining artificially fresh until that time.
func knownBackupStalenessMetric(
	ctx context.Context,
	name string,
	now time.Time,
	at time.Time,
	threshold time.Duration,
	detail string,
) backupStalenessMetric {
	if lead := at.Sub(now); lead > backupStalenessFutureTolerance {
		telemetry.L(ctx).ErrorContext(
			ctx,
			"backup.staleness.timestamp_implausible",
			slog.String("metric", name),
			slog.Time("at", at.UTC()),
			slog.Time("now", now.UTC()),
			slog.String("lead", backupStalenessSeconds(lead)),
			slog.String("tolerance", backupStalenessSeconds(backupStalenessFutureTolerance)),
		)

		return unknownBackupStalenessMetric(
			name,
			threshold,
			backupStalenessUnreadable,
			implausibleFutureDetail(at, lead, detail),
		)
	}

	return backupStalenessMetric{
		Name:      name,
		At:        at.UTC(),
		Age:       backupStalenessAge(now, at),
		AgeKnown:  true,
		Unknown:   backupStalenessAgeKnown,
		Threshold: threshold,
		Detail:    detail,
	}
}

// implausibleFutureDetail reports the rejected timestamp with its marker or
// run provenance to identify the incorrect clock.
func implausibleFutureDetail(at time.Time, lead time.Duration, provenance string) string {
	detail := fmt.Sprintf(
		"success timestamp %s is %s in the future, past the %s clock-skew tolerance",
		at.UTC().Format(time.RFC3339),
		backupStalenessSeconds(lead),
		backupStalenessSeconds(backupStalenessFutureTolerance),
	)
	if provenance == "" {
		return detail
	}

	return detail + ": " + provenance
}

// unknownBackupStalenessMetric records an undatable success with a cause and
// actionable diagnostic detail.
func unknownBackupStalenessMetric(
	name string,
	threshold time.Duration,
	cause backupStalenessUnknownCause,
	detail string,
) backupStalenessMetric {
	return backupStalenessMetric{
		Name:      name,
		At:        time.Time{},
		Age:       0,
		AgeKnown:  false,
		Unknown:   cause,
		Threshold: threshold,
		Detail:    detail,
	}
}

// backupStalenessAge floors accepted clock skew at zero. Timestamp validation
// rejects larger future leads before calculating age.
func backupStalenessAge(now, at time.Time) time.Duration {
	age := now.Sub(at)
	if age < 0 {
		return 0
	}

	return age
}

// backupStalenessThreshold converts configured seconds to a duration.
func backupStalenessThreshold(seconds int) time.Duration {
	return time.Duration(seconds) * time.Second
}

// backupStalenessReport prints a searchable line per mechanism with age,
// threshold, verdict, and provenance for terminal and journal use.
func backupStalenessReport(metrics []backupStalenessMetric) string {
	width := 0

	for _, metric := range metrics {
		if len(metric.Name) > width {
			width = len(metric.Name)
		}
	}

	var report strings.Builder

	for _, metric := range metrics {
		fmt.Fprintf(
			&report,
			"%-*s age=%s threshold=%s %s",
			width,
			metric.Name,
			backupStalenessAgeField(metric),
			backupStalenessSeconds(metric.Threshold),
			metric.verdict(),
		)
		if detail := backupStalenessDetailField(metric); detail != "" {
			report.WriteString(" " + detail)
		}

		report.WriteString("\n")
	}

	return report.String()
}

// backupStalenessAgeField prints the age or the unknown label.
func backupStalenessAgeField(metric backupStalenessMetric) string {
	if !metric.AgeKnown {
		return "unknown"
	}

	return backupStalenessSeconds(metric.Age)
}

// backupStalenessDetailField flattens provenance and external errors to one
// line per mechanism.
func backupStalenessDetailField(metric backupStalenessMetric) string {
	return strings.Join(strings.Fields(metric.Detail), " ")
}

// backupStalenessSeconds prints whole seconds, matching configured thresholds.
func backupStalenessSeconds(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10) + "s"
}

// staleBackupStalenessMetrics lists stale metrics in report order for the
// command's final error.
func staleBackupStalenessMetrics(metrics []backupStalenessMetric) []string {
	var stale []string

	for _, metric := range metrics {
		if metric.stale() {
			stale = append(stale, metric.Name)
		}
	}

	return stale
}
