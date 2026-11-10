// Backup staleness classification uses each mechanism's last success, its
// age, and the operator's threshold to produce a freshness verdict and
// report. Classification reads supplied values without querying an object
// store, a cluster, or the clock. Tests exercise classification with plain
// values. The probes supply the timestamps.

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
	// backupStalenessExportName identifies the off-host ledger export, a
	// YugabyteDB distributed-snapshot run in the object store.
	backupStalenessExportName = "ledger-export"
	// backupStalenessRehearsalName identifies the restore drill, which checks the
	// documented recovery procedure against real backups.
	backupStalenessRehearsalName = "rehearsal"
	// backupStalenessReplicationName is the live cluster's replication health.
	backupStalenessReplicationName = "replication"
	// backupStalenessFDBName is the FoundationDB continuous backup's
	// restorable point.
	backupStalenessFDBName = "fdb-restorable"
)

// backupStalenessFutureTolerance limits how far a success timestamp can
// exceed the checking host's clock. Markers use object-store JSON, export run
// keys supply timestamps, and FoundationDB reports its cluster's restorable
// point. These sources can use other hosts' clocks. Small clock differences
// can account for a lead of seconds or a few minutes. The check rejects a
// larger lead as untrustworthy; it cannot determine whether a clock error or
// forgery caused that lead. A healthy local replication probe uses this
// host's current time.
const backupStalenessFutureTolerance = 5 * time.Minute

// backupStalenessMetric records one mechanism's last success, age, threshold,
// and reading source. At records the last success in UTC and remains zero
// when the age is unknown. Unknown records the cause. An unreadable current
// reading can use this guest's earlier reading for a known age while
// preserving the cause. LastReadAt records when the guest read that earlier
// success.
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

// stale reports whether a mechanism has an unknown age or an age greater
// than its threshold. An unknown age does not establish a recent success;
// the check treats it as stale to detect silent backup failures.
func (m backupStalenessMetric) stale() bool {
	if !m.AgeKnown {
		return true
	}

	return m.Age > m.Threshold
}

// verdict returns the word that the report prints for this metric.
func (m backupStalenessMetric) verdict() string {
	if m.stale() {
		return "STALE"
	}

	return "FRESH"
}

// knownBackupStalenessMetric builds a metric from a success timestamp. It
// checks the supplied timestamp against the future tolerance before
// calculating its age. A timestamp beyond the tolerance produces an
// unreadable metric with an unknown age, which classifies as stale.
//
// Without this check, backupStalenessAge would return zero for a future
// timestamp. The report would classify that timestamp as FRESH against every
// threshold until the checking clock caught up.
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

// implausibleFutureDetail explains why the check rejected a success
// timestamp. It preserves the reading source to identify the marker or run
// with the untrustworthy timestamp.
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

// unknownBackupStalenessMetric builds a stale metric without a usable
// success timestamp. The cause distinguishes unknown states, and detail
// explains the problem that the operator must assess.
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

// backupStalenessAge returns the time since at as of now, with zero as the
// minimum age. Small future timestamps within the tolerance count as a new
// success instead of producing a negative age. knownBackupStalenessMetric
// rejects larger leads before calling this helper.
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

// backupStalenessReport renders one line per metric with its mechanism,
// age, threshold, and verdict, followed by the reading source. The terminal
// and journal use this searchable plain-text form. Alarm mail uses ordinary
// words for these facts.
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

// backupStalenessAgeField renders a metric's age. It returns "unknown" when
// the metric cannot date the mechanism's last success.
func backupStalenessAgeField(metric backupStalenessMetric) string {
	if !metric.AgeKnown {
		return "unknown"
	}

	return backupStalenessSeconds(metric.Age)
}

// backupStalenessDetailField converts the reading source and detail to one
// line. Store or cluster errors can include whitespace; the report uses one
// line per mechanism.
func backupStalenessDetailField(metric backupStalenessMetric) string {
	return strings.Join(strings.Fields(metric.Detail), " ")
}

// backupStalenessSeconds renders a duration in whole seconds, matching the
// configured threshold unit.
func backupStalenessSeconds(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10) + "s"
}

// staleBackupStalenessMetrics returns stale metric names in report order
// for the command's error.
func staleBackupStalenessMetrics(metrics []backupStalenessMetric) []string {
	var stale []string

	for _, metric := range metrics {
		if metric.stale() {
			stale = append(stale, metric.Name)
		}
	}

	return stale
}
