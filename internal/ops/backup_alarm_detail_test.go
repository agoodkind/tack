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

// backupAlarmNumberedArgument is the marker a format string uses to number its
// arguments. Go prints no complaint when a numbered format string is handed an
// argument it does not name, so an argument dropped from one of these
// sentences vanished without a trace: that is how the last-check sentence
// reached the ledger cluster's mail and no other. Without the numbering, the
// same mistake prints "%!(EXTRA ...)" in the sentence itself.
const backupAlarmNumberedArgument = "%["

// TestBackupAlarmWordsNumberNoArgument fails on any sentence that numbers its
// arguments, because numbering is what hides a composer handing the words more
// than they name.
func TestBackupAlarmWordsNumberNoArgument(t *testing.T) {
	for _, name := range backupAlarmMetricNames {
		words := backupAlarmVocabulary[name]
		for label, template := range map[string]string{
			"paragraphKnown":         words.paragraphKnown,
			"paragraphNeverRecorded": words.paragraphNeverRecorded,
			"paragraphUnreadable":    words.paragraphUnreadable,
			"paragraphRemembered":    words.paragraphRemembered,
			"phraseKnown":            words.phraseKnown,
		} {
			if strings.Contains(template, backupAlarmNumberedArgument) {
				t.Errorf("%s %s numbers its arguments, which hides an argument the words do not name: %q",
					name, label, template)
			}
		}
	}
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
		fault := knownBackupStalenessMetric(ctx, name, now, now.Add(-40*time.Hour), 36*time.Hour, reading)
		body := backupStalenessAlarmBody(cfg, []backupStalenessMetric{fault})

		if strings.Contains(body, "%!") {
			t.Errorf("%s: the mail carries a formatting marker, so a sentence and its arguments disagree:\n%s",
				name, body)
		}
		declared := backupAlarmVocabulary[name].paragraphKnownDetail != ""
		carried := strings.Contains(body, "The last check reported: "+reading+".")
		if declared && !carried {
			t.Errorf("%s: the words promise the last-check sentence and the mail does not carry it:\n%s", name, body)
		}
		if !declared && carried {
			t.Errorf("%s: the mail claims the last check reported a reading its words never promised:\n%s", name, body)
		}
	}
}

// TestBackupAlarmLastCheckSentenceGoesWhereTheReadingIsThisRun pins which
// mechanism declares the sentence and why. Only the ledger cluster's known-age
// detail is what this run observed: the check probes the cluster and replaces
// the detail with its own reading. The other three carry the provenance of the
// last recorded success instead, so "the last check reported" would be a false
// sentence on their mails.
func TestBackupAlarmLastCheckSentenceGoesWhereTheReadingIsThisRun(t *testing.T) {
	for _, name := range backupAlarmMetricNames {
		declared := backupAlarmVocabulary[name].paragraphKnownDetail != ""
		wantDeclared := name == backupStalenessReplicationName
		if declared != wantDeclared {
			t.Errorf("%s declares the last-check sentence = %v, want %v", name, declared, wantDeclared)
		}
	}
}
