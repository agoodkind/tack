// Alarm vocabulary separates human-readable fault explanations and recovery
// steps from machine metric identifiers, raw seconds, and run IDs.

package ops

// backupAlarmWords defines message templates. Known paragraphs accept success
// time, age, and threshold. Never-recorded paragraphs accept sanitized detail;
// unreadable paragraphs exclude client errors. Historical unreadable readings
// add the prior observation's time, age, and threshold.
//
// paragraphKnownDetail accepts live observation detail only for replication.
// Export run IDs, rehearsal provenance, and restorable points cannot truthfully
// appear as "the last check reported" observations. Unnumbered format arguments
// preserve Go's extra-argument diagnostics for mismatched templates.
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

// backupAlarmVocabulary selects each mechanism's human-readable messages.
var backupAlarmVocabulary = map[string]backupAlarmWords{
	backupStalenessExportName: {
		phraseKnown:            "Nightly ledger export is %s old",
		phraseNeverRecorded:    "Nightly ledger export has never completed",
		phraseUnreadable:       "This guest could not read nightly ledger export status",
		paragraphKnown:         backupAlarmExportNoun + " last completed" + backupAlarmKnownFact,
		paragraphKnownDetail:   "",
		paragraphNeverRecorded: backupAlarmExportNoun + " has never completed." + backupAlarmLastCheck,
		paragraphUnreadable:    "This guest could not date the nightly ledger export's newest copy.",
		paragraphRemembered:    " The most recent export this guest read completed" + backupAlarmKnownFact,
		steps: []string{
			"On the owner guest, run journalctl -u tack-ledger-export.",
			"On each data guest, run journalctl -u tack-ledger-archive.",
			"Confirm the object store accepts writes, then run systemctl start tack-ledger-export.",
		},
	},
	backupStalenessRehearsalName: {
		phraseKnown:            "Restore rehearsal has not passed in %s",
		phraseNeverRecorded:    "Restore rehearsal has never passed",
		phraseUnreadable:       "This guest could not read restore rehearsal status",
		paragraphKnown:         backupAlarmRehearsalNoun + " last passed" + backupAlarmKnownFact,
		paragraphKnownDetail:   "",
		paragraphNeverRecorded: backupAlarmRehearsalNoun + " has never passed." + backupAlarmLastCheck,
		paragraphUnreadable:    "This guest could not read the restore rehearsal's last pass.",
		paragraphRemembered:    " The newest pass this guest last read was" + backupAlarmKnownFact,
		steps: []string{
			"On the owner guest, run journalctl -u tack-backup-restore-drill.",
			"Resolve the rehearsal failure, then run systemctl start tack-backup-restore-drill.",
		},
	},
	backupStalenessReplicationName: {
		phraseKnown:          "Ledger cluster unhealthy for %s",
		phraseNeverRecorded:  "This guest has never observed a healthy ledger cluster",
		phraseUnreadable:     "This guest could not read ledger cluster health status",
		paragraphKnown:       backupAlarmReplicationNoun + " was last healthy" + backupAlarmKnownFact,
		paragraphKnownDetail: backupAlarmLastCheck,
		paragraphNeverRecorded: "This guest has never observed the ledger cluster " +
			"(logins and audit trail) healthy." + backupAlarmLastCheck,
		paragraphUnreadable: "This guest could not read the ledger cluster's last healthy observation.",
		paragraphRemembered: " This guest last recorded it healthy" + backupAlarmKnownFact,
		steps: []string{
			"Confirm every ledger guest is up.",
			"On the owner guest, confirm every node is alive on the ledger master page.",
			"Wait for tablets to re-copy; the alarm clears itself once the cluster is healthy.",
		},
	},
	backupStalenessFDBName: {
		phraseKnown:            "Product database backup stopped %s ago",
		phraseNeverRecorded:    "Product database backup does not have a restorable point",
		phraseUnreadable:       "This guest could not read product database backup status",
		paragraphKnown:         backupAlarmFDBNoun + " last advanced" + backupAlarmKnownFact,
		paragraphKnownDetail:   "",
		paragraphNeverRecorded: backupAlarmFDBNoun + " does not have a restorable point." + backupAlarmLastCheck,
		paragraphUnreadable:    "This guest could not read the product database backup's status.",
		paragraphRemembered:    " The newest restorable point this guest last read was" + backupAlarmKnownFact,
		steps: []string{
			"On each data guest, run docker ps and confirm tack-fdb-backup-agent-1 is there.",
			"On a data guest that is missing it, run docker logs tack-fdb-backup-agent-1 and read what it reports.",
			"Confirm the object store accepts writes, then run docker compose restart fdb-backup-agent on that guest.",
		},
	},
}
