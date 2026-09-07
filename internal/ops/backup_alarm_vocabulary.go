// backup_alarm_vocabulary.go is each backup mechanism's share of the alarm
// mail: its plain name, the subject phrase for each kind of fault, the
// paragraph that says what happened, and the steps that fix it. Metric names,
// thresholds in seconds, and run identifiers stay in the journal report; none
// of them appear here. The composition is in backup_alarm_words.go.

package ops

// backupAlarmWords is one mechanism's vocabulary. The known templates take the
// last-good time, the age, the limit, and the detail, in that order. An unknown
// age has two vocabularies, chosen by the metric's cause, because a success
// that was never recorded and a record that could not be read support
// different claims; both templates take the detail alone.
type backupAlarmWords struct {
	name                   string
	subjectKnown           string
	subjectNeverRecorded   string
	subjectUnreadable      string
	paragraphKnown         string
	paragraphNeverRecorded string
	paragraphUnreadable    string
	steps                  []string
}

const (
	backupAlarmExportNoun      = "The nightly ledger export (the daily copy of the ledger database saved to the object store)"
	backupAlarmRehearsalNoun   = "The restore rehearsal (the daily test restore of both databases from the object store)"
	backupAlarmReplicationNoun = "The ledger cluster (the database holding logins and the audit trail)"
	backupAlarmFDBNoun         = "The product database's continuous backup (FoundationDB)"
	backupAlarmLastCheckSaw    = " The last check saw: %[1]s."
)

// backupAlarmVocabulary maps each metric to its words.
var backupAlarmVocabulary = map[string]backupAlarmWords{
	backupStalenessExportName: {
		name:                 "Nightly ledger export",
		subjectKnown:         "Nightly ledger export is %s old",
		subjectNeverRecorded: "Nightly ledger export has never completed",
		subjectUnreadable:    "Nightly ledger export status could not be read",
		paragraphKnown: backupAlarmExportNoun + " last completed at %[1]s, %[2]s ago. " +
			"The allowed limit is %[3]s.",
		paragraphNeverRecorded: backupAlarmExportNoun + " has never completed, so there is no copy " +
			"to restore the ledger from." + backupAlarmLastCheckSaw,
		paragraphUnreadable: backupAlarmExportNoun + " could not be dated, so whether a current copy " +
			"exists is not known." + backupAlarmLastCheckSaw,
		steps: []string{
			"On the owner guest, read the export journal: journalctl -u tack-ledger-export.",
			"On each data guest, read the archive journal: journalctl -u tack-ledger-archive.",
			"Confirm the object store accepts writes, then start an export: systemctl start tack-ledger-export.",
		},
	},
	backupStalenessRehearsalName: {
		name:                 "Restore rehearsal",
		subjectKnown:         "Restore rehearsal has not passed in %s",
		subjectNeverRecorded: "Restore rehearsal has never passed",
		subjectUnreadable:    "Restore rehearsal status could not be read",
		paragraphKnown: backupAlarmRehearsalNoun + " last passed at %[1]s, %[2]s ago. " +
			"The allowed limit is %[3]s.",
		paragraphNeverRecorded: backupAlarmRehearsalNoun + " has never passed." + backupAlarmLastCheckSaw,
		paragraphUnreadable: backupAlarmRehearsalNoun + " status could not be read, so whether it has " +
			"passed recently is not known." + backupAlarmLastCheckSaw,
		steps: []string{
			"On the owner guest, read the drill journal: journalctl -u tack-backup-restore-drill.",
			"Fix what it names, then run the drill: systemctl start tack-backup-restore-drill.",
		},
	},
	backupStalenessReplicationName: {
		name:                 "Ledger cluster",
		subjectKnown:         "Ledger cluster unhealthy for %s",
		subjectNeverRecorded: "Ledger cluster has never been seen healthy",
		subjectUnreadable:    "Ledger cluster health status could not be read",
		paragraphKnown: backupAlarmReplicationNoun + " was last seen healthy at %[1]s, %[2]s ago. " +
			"The allowed limit is %[3]s. The last check saw: %[4]s.",
		paragraphNeverRecorded: backupAlarmReplicationNoun + " has never been seen healthy." +
			backupAlarmLastCheckSaw,
		paragraphUnreadable: backupAlarmReplicationNoun + " health record could not be read, so how long " +
			"it has been unhealthy is not known." + backupAlarmLastCheckSaw,
		steps: []string{
			"Check every ledger guest is running and reachable.",
			"On the owner guest, open the ledger master page and confirm every node is alive.",
			"Wait for tablets to finish copying; this alarm clears by itself once the cluster is healthy.",
		},
	},
	backupStalenessFDBName: {
		name:                 "Product database backup",
		subjectKnown:         "Product database backup stopped %s ago",
		subjectNeverRecorded: "Product database backup has no restorable point",
		subjectUnreadable:    "Product database backup status could not be read",
		paragraphKnown: backupAlarmFDBNoun + " last advanced at %[1]s, %[2]s ago. " +
			"The allowed limit is %[3]s. Writes since that point cannot yet be restored from the object store.",
		paragraphNeverRecorded: backupAlarmFDBNoun + " has no restorable point yet, so nothing can be " +
			"restored from it." + backupAlarmLastCheckSaw,
		paragraphUnreadable: backupAlarmFDBNoun + " status could not be read, so whether recent writes " +
			"are restorable is not known." + backupAlarmLastCheckSaw,
		steps: []string{
			"On the owner guest, check the backup agent is running: docker ps (container tack-fdb-backup-agent-1).",
			"Read its log: docker logs tack-fdb-backup-agent-1.",
			"Confirm the object store accepts writes, then restart the agent: docker compose restart fdb-backup-agent.",
		},
	},
}
