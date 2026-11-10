package ops

import (
	"context"
	"strings"
	"testing"
	"time"
)

// backupAlarmMetricNames lists the mechanisms with alarm vocabulary in the
// order that the check reports them.
var backupAlarmMetricNames = []string{
	backupStalenessExportName,
	backupStalenessRehearsalName,
	backupStalenessReplicationName,
	backupStalenessFDBName,
}

// backupAlarmNumberedArgument marks an explicit argument index in a format
// string. Go does not report unused arguments with indexed formatting. The
// earlier alarm text silently omitted the last-check sentence from every
// mechanism except the ledger cluster. This test retains that regression case.
// Unindexed formatting reports an unused argument as "%!(EXTRA ...)".
const backupAlarmNumberedArgument = "%["

// TestBackupAlarmWordsNumberNoArgument rejects explicit argument indexes in
// alarm templates. Indexed formatting can hide unused arguments.
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
// of the four mechanisms. A template that includes the last-check sentence must
// report this run's reading. Every message must omit formatting error markers,
// which indicate a mismatch between the format string and its arguments.
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

// TestBackupAlarmLastCheckSentenceGoesWhereTheReadingIsThisRun checks that
// only the ledger cluster uses the last-check sentence. The check probes the
// cluster and replaces its detail with the current observation. The other
// three mechanisms report the origin of their last recorded success; calling
// that information this run's reading would misstate its time.
func TestBackupAlarmLastCheckSentenceGoesWhereTheReadingIsThisRun(t *testing.T) {
	for _, name := range backupAlarmMetricNames {
		declared := backupAlarmVocabulary[name].paragraphKnownDetail != ""
		wantDeclared := name == backupStalenessReplicationName
		if declared != wantDeclared {
			t.Errorf("%s declares the last-check sentence = %v, want %v", name, declared, wantDeclared)
		}
	}
}
