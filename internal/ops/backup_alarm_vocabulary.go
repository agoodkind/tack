// backup_alarm_vocabulary.go is each backup mechanism's share of the alarm
// mail: its plain name, the subject phrase for each kind of fault, the one
// sentence that says what happened, and the steps that fix it. Metric names,
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
	backupAlarmExportNoun      = "The nightly ledger export (the daily copy of the ledger in the object store)"
	backupAlarmRehearsalNoun   = "The restore rehearsal (the daily test restore)"
	backupAlarmReplicationNoun = "The ledger cluster (logins and audit trail)"
	backupAlarmFDBNoun         = "The product database backup (FoundationDB, continuous)"
	backupAlarmKnownFact       = " at %[1]s, %[2]s ago; the limit is %[3]s."
	backupAlarmLastCheck       = " The last check reported: %[1]s."
)

// backupAlarmVocabulary maps each metric to its words.
var backupAlarmVocabulary = map[string]backupAlarmWords{
	backupStalenessExportName: {
		name:                   "Nightly ledger export",
		subjectKnown:           "Nightly ledger export is %s old",
		subjectNeverRecorded:   "Nightly ledger export has never completed",
		subjectUnreadable:      "Nightly ledger export status could not be read",
		paragraphKnown:         backupAlarmExportNoun + " last completed" + backupAlarmKnownFact,
		paragraphNeverRecorded: backupAlarmExportNoun + " has never completed." + backupAlarmLastCheck,
		paragraphUnreadable:    "The nightly ledger export's newest copy could not be dated." + backupAlarmLastCheck,
		steps: []string{
			"On the owner guest, run journalctl -u tack-ledger-export.",
			"On each data guest, run journalctl -u tack-ledger-archive.",
			"Confirm the object store accepts writes, then run systemctl start tack-ledger-export.",
		},
	},
	backupStalenessRehearsalName: {
		name:                   "Restore rehearsal",
		subjectKnown:           "Restore rehearsal has not passed in %s",
		subjectNeverRecorded:   "Restore rehearsal has never passed",
		subjectUnreadable:      "Restore rehearsal status could not be read",
		paragraphKnown:         backupAlarmRehearsalNoun + " last passed" + backupAlarmKnownFact,
		paragraphNeverRecorded: backupAlarmRehearsalNoun + " has never passed." + backupAlarmLastCheck,
		paragraphUnreadable:    "The restore rehearsal's last pass could not be read." + backupAlarmLastCheck,
		steps: []string{
			"On the owner guest, run journalctl -u tack-backup-restore-drill.",
			"Fix what it names, then run systemctl start tack-backup-restore-drill.",
		},
	},
	backupStalenessReplicationName: {
		name:                   "Ledger cluster",
		subjectKnown:           "Ledger cluster unhealthy for %s",
		subjectNeverRecorded:   "Ledger cluster has never been seen healthy",
		subjectUnreadable:      "Ledger cluster health status could not be read",
		paragraphKnown:         backupAlarmReplicationNoun + " was last healthy" + backupAlarmKnownFact + " The last check reported: %[4]s.",
		paragraphNeverRecorded: backupAlarmReplicationNoun + " has never been seen healthy." + backupAlarmLastCheck,
		paragraphUnreadable:    "The ledger cluster's last healthy reading could not be read." + backupAlarmLastCheck,
		steps: []string{
			"Confirm every ledger guest is up.",
			"On the owner guest, confirm every node is alive on the ledger master page.",
			"Wait for tablets to re-copy; the alarm clears itself once the cluster is healthy.",
		},
	},
	backupStalenessFDBName: {
		name:                   "Product database backup",
		subjectKnown:           "Product database backup stopped %s ago",
		subjectNeverRecorded:   "Product database backup has no restorable point",
		subjectUnreadable:      "Product database backup status could not be read",
		paragraphKnown:         backupAlarmFDBNoun + " last advanced" + backupAlarmKnownFact,
		paragraphNeverRecorded: backupAlarmFDBNoun + " has no restorable point." + backupAlarmLastCheck,
		paragraphUnreadable:    "The product database backup's status could not be read." + backupAlarmLastCheck,
		steps: []string{
			"On the owner guest, run docker ps and confirm tack-fdb-backup-agent-1 is running.",
			"Run docker logs tack-fdb-backup-agent-1 and read what it reports.",
			"Confirm the object store accepts writes, then run docker compose restart fdb-backup-agent.",
		},
	},
}
