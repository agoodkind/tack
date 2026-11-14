package ops

import (
	"context"
	"strings"
	"testing"
	"time"
)

// backupAlarmMetricNames lists mechanisms in report order.
var backupAlarmMetricNames = []string{
	backupStalenessExportName,
	backupStalenessRehearsalName,
	backupStalenessReplicationName,
	backupStalenessFDBName,
}

// TestBackupAlarmKnownMailFormatsDeclaredDetails checks each mechanism's known-age
// alarm for formatting errors and the declared last-check detail.
func TestBackupAlarmKnownMailFormatsDeclaredDetails(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")
	const reading = "1 dead nodes, 4 under-replicated tablets"

	for _, name := range backupAlarmMetricNames {
		fault := knownBackupStalenessMetric(
			ctx,
			name,
			now,
			now.Add(-40*time.Hour),
			36*time.Hour,
			reading,
		)
		body := backupStalenessAlarmBody(cfg, []backupStalenessMetric{fault})

		if strings.Contains(body, "%!") {
			t.Errorf(
				"%s: the mail contains a formatting marker from mismatched arguments:\n%s",
				name,
				body,
			)
		}

		declared := backupAlarmVocabulary[name].paragraphKnownDetail != ""
		carried := strings.Contains(body, "The last check reported: "+reading+".")
		if declared && !carried {
			t.Errorf(
				"%s: the mail does not include the declared last-check sentence:\n%s",
				name,
				body,
			)
		}

		if !declared && carried {
			t.Errorf(
				"%s: the mail includes a last-check sentence without a declaration:\n%s",
				name,
				body,
			)
		}
	}
}
