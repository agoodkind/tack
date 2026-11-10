// Backup alarm vocabulary supplies each mechanism's fault phrase, factual
// sentence, and recovery steps. The journal report includes metric identifiers,
// thresholds in seconds, and run identifiers. The alarm composer uses these
// words without those report details.

package ops

// backupAlarmWords supplies one mechanism's alarm vocabulary. Known-age
// templates take the last-good time, age, and limit in that order. Unknown-age
// templates distinguish an unrecorded success from an unreadable record. The
// never-recorded template takes detail alone. The unreadable template does not
// take arguments: client errors appear only in the journal. An optional
// remembered sentence takes the last-good time, age, and limit from this
// guest's previous reading.
//
// paragraphKnownDetail appends this check's observation to a known-age
// paragraph and takes detail alone. Only the ledger cluster check supplies a
// live observation. Other mechanisms report the provenance of an earlier
// success, such as a run identifier or a restorable point; calling that detail
// "the last check reported" would misstate its source. Every mechanism sets
// paragraphKnownDetail explicitly, including an empty value when it does not
// apply.
//
// An earlier composer supplied detail to all known-age templates, but only the
// ledger cluster template used it. Explicit argument indexes suppressed Go's
// "%!(EXTRA ...)" marker for unused arguments. These templates omit argument
// indexes to expose that mistake.
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
