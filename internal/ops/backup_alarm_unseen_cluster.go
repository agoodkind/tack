// Unanswered master probes cannot establish degraded cluster health. The
// alarm reports this guest's missing health evidence, not a cluster-wide
// replication failure (TACK-529).

package ops

// backupAlarmUnseenClusterWords uses unreadable and optional historical
// observations. Missing current evidence cannot establish a fresh success or
// a definitive never-healthy cluster verdict.
var backupAlarmUnseenClusterWords = backupAlarmWords{
	phraseKnown:            "",
	phraseNeverRecorded:    "",
	phraseUnreadable:       "This guest cannot read ledger cluster health",
	paragraphKnown:         "",
	paragraphKnownDetail:   "",
	paragraphNeverRecorded: "",
	paragraphUnreadable: "This guest could not read a usable health report from the ledger cluster " +
		"(logins and audit trail). " +
		"This check cannot establish whether the cluster is healthy.",
	paragraphRemembered: " This guest last observed the cluster healthy" + backupAlarmKnownFact,
	steps: []string{
		"Confirm every ledger guest is up.",
		"From this guest, confirm the ledger master page answers.",
		"Resolve the health-check failure; the checker clears the alarm after this guest reads a healthy response.",
	},
}

// backupAlarmWordsFor selects observation-failure wording for unseen clusters
// instead of implying degradation without a valid master response.
func backupAlarmWordsFor(fault backupStalenessMetric) backupAlarmWords {
	if fault.Unknown == backupStalenessClusterUnseen {
		return backupAlarmUnseenClusterWords
	}

	return backupAlarmVocabulary[fault.Name]
}
