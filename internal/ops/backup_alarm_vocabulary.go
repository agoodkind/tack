// backup_alarm_vocabulary.go is each backup mechanism's share of the alarm
// mail: the phrase that names each kind of fault, the one sentence that says
// what happened, and the steps that fix it. Metric names, thresholds in
// seconds, and run identifiers stay in the journal report; none of them appear
// here. The composition is in backup_alarm_words.go.

package ops

// backupAlarmWords is one mechanism's vocabulary. The known template takes the
// last-good time, the age, and the limit, in that order. An unknown age has two
// vocabularies, chosen by the metric's cause, because a success that was never
// recorded and a record that could not be read support different claims. The
// never-recorded template takes the detail alone. The unreadable sentence takes
// nothing, because the only detail is a client's error text, which stays in the
// journal; the remembered sentence that may follow it takes the last-good time,
// the age, and the limit of the last reading this guest took.
//
// paragraphKnownDetail is the sentence appended to a known-age paragraph that
// names what the reading showed, and it takes the detail alone. It is empty on
// every mechanism whose known-age detail is the provenance of the last recorded
// success rather than an observation this run made, because "the last check
// reported" would be a false claim about a run identifier or a restorable
// point. Only the ledger cluster's check replaces the detail with its own live
// reading (replicationStalenessMetric in backup_staleness_replication.go), so
// only that mechanism declares the sentence. Every mechanism declares this field one
// way or the other: a composer that handed these words an argument they do not
// name printed nothing where it was dropped, which is how the sentence reached
// one mail and no other.
//
// No template numbers its arguments. A numbered format string is what silenced
// that mistake, because Go prints its "%!(EXTRA ...)" marker only for a format
// string that does not.
type backupAlarmWords struct {
	phraseKnown            string
	phraseNeverRecorded    string
	phraseUnreadable       string
	paragraphKnown         string
	paragraphKnownDetail   string
	paragraphNeverRecorded string
	paragraphUnreadable    string
	paragraphRemembered    string
	steps                  []string
}

const (
	backupAlarmExportNoun      = "The nightly ledger export (the daily copy of the ledger in the object store)"
	backupAlarmRehearsalNoun   = "The restore rehearsal (the daily test restore)"
	backupAlarmReplicationNoun = "The ledger cluster (logins and audit trail)"
	backupAlarmFDBNoun         = "The product database backup (FoundationDB, continuous)"
	backupAlarmKnownFact       = " at %s, %s ago; the limit is %s."
	backupAlarmLastCheck       = " The last check reported: %s."
)

// backupAlarmVocabulary maps each metric to its words.
var backupAlarmVocabulary = map[string]backupAlarmWords{
	backupStalenessExportName: {
		phraseKnown:            "Nightly ledger export is %s old",
		phraseNeverRecorded:    "Nightly ledger export has never completed",
		phraseUnreadable:       "Nightly ledger export status could not be read",
		paragraphKnown:         backupAlarmExportNoun + " last completed" + backupAlarmKnownFact,
		paragraphKnownDetail:   "",
		paragraphNeverRecorded: backupAlarmExportNoun + " has never completed." + backupAlarmLastCheck,
		paragraphUnreadable:    "The nightly ledger export's newest copy could not be dated.",
		paragraphRemembered:    " The newest copy this guest last read was made" + backupAlarmKnownFact,
		steps: []string{
			"On the owner guest, run journalctl -u tack-ledger-export.",
			"On each data guest, run journalctl -u tack-ledger-archive.",
			"Confirm the object store accepts writes, then run systemctl start tack-ledger-export.",
		},
	},
	backupStalenessRehearsalName: {
		phraseKnown:            "Restore rehearsal has not passed in %s",
		phraseNeverRecorded:    "Restore rehearsal has never passed",
		phraseUnreadable:       "Restore rehearsal status could not be read",
		paragraphKnown:         backupAlarmRehearsalNoun + " last passed" + backupAlarmKnownFact,
		paragraphKnownDetail:   "",
		paragraphNeverRecorded: backupAlarmRehearsalNoun + " has never passed." + backupAlarmLastCheck,
		paragraphUnreadable:    "The restore rehearsal's last pass could not be read.",
		paragraphRemembered:    " The newest pass this guest last read was" + backupAlarmKnownFact,
		steps: []string{
			"On the owner guest, run journalctl -u tack-backup-restore-drill.",
			"Fix what it names, then run systemctl start tack-backup-restore-drill.",
		},
	},
	backupStalenessReplicationName: {
		phraseKnown:            "Ledger cluster unhealthy for %s",
		phraseNeverRecorded:    "Ledger cluster has never been seen healthy",
		phraseUnreadable:       "Ledger cluster health status could not be read",
		paragraphKnown:         backupAlarmReplicationNoun + " was last healthy" + backupAlarmKnownFact,
		paragraphKnownDetail:   backupAlarmLastCheck,
		paragraphNeverRecorded: backupAlarmReplicationNoun + " has never been seen healthy." + backupAlarmLastCheck,
		paragraphUnreadable:    "The ledger cluster's last healthy reading could not be read.",
		paragraphRemembered:    " This guest last recorded it healthy" + backupAlarmKnownFact,
		steps: []string{
			"Confirm every ledger guest is up.",
			"On the owner guest, confirm every node is alive on the ledger master page.",
			"Wait for tablets to re-copy; the alarm clears itself once the cluster is healthy.",
		},
	},
	backupStalenessFDBName: {
		phraseKnown:            "Product database backup stopped %s ago",
		phraseNeverRecorded:    "Product database backup has no restorable point",
		phraseUnreadable:       "Product database backup status could not be read",
		paragraphKnown:         backupAlarmFDBNoun + " last advanced" + backupAlarmKnownFact,
		paragraphKnownDetail:   "",
		paragraphNeverRecorded: backupAlarmFDBNoun + " has no restorable point." + backupAlarmLastCheck,
		paragraphUnreadable:    "The product database backup's status could not be read.",
		paragraphRemembered:    " The newest restorable point this guest last read was" + backupAlarmKnownFact,
		steps: []string{
			"On each data guest, run docker ps and confirm tack-fdb-backup-agent-1 is there.",
			"On a data guest that is missing it, run docker logs tack-fdb-backup-agent-1 and read what it reports.",
			"Confirm the object store accepts writes, then run docker compose restart fdb-backup-agent on that guest.",
		},
	},
}
