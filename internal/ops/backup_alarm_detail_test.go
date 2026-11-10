package ops

import (
	"context"
	"strings"
	"testing"
	"time"
)

// backupAlarmMetricNames is every mechanism the alarm has words for, in the
// order the check reports them.
var backupAlarmMetricNames = []string{
	backupStalenessExportName,
	backupStalenessRehearsalName,
	backupStalenessReplicationName,
	backupStalenessFDBName,
}

// TestBackupAlarmKnownMailsDropNoArgument composes a known-age fault for each
// of the four mechanisms and reads the sentence back. Every mechanism whose
// words end in the last-check sentence must name this run's reading, and no
// mechanism may print a formatting marker, which is what a sentence handed the
// wrong number of arguments now shows.
func TestBackupAlarmKnownMailsDropNoArgument(t *testing.T) {
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
				"%s: the mail carries a formatting marker, so a sentence and its arguments disagree:\n%s",
				name,
				body,
			)
		}

		declared := backupAlarmVocabulary[name].paragraphKnownDetail != ""
		carried := strings.Contains(body, "The last check reported: "+reading+".")
		if declared && !carried {
			t.Errorf(
				"%s: the words promise the last-check sentence and the mail does not carry it:\n%s",
				name,
				body,
			)
		}

		if !declared && carried {
			t.Errorf(
				"%s: the mail claims the last check reported a reading its words never promised:\n%s",
				name,
				body,
			)
		}
	}
}
