// backup_alarm_unseen_cluster.go is the alarm mail of a guest that reached no
// ledger master. The check alarms on what the guest running it can see for
// itself, so that guest must say it cannot see the cluster and must not say
// the cluster is unhealthy: only a master's own answer supports that claim
// (TACK-529). Every other fault keeps the words in backup_alarm_vocabulary.go.

package ops

// backupAlarmUnseenClusterWords is the vocabulary of a cluster this guest
// could not see. The unseen cause never carries a fresh success, so the known
// and never-recorded templates have nothing to say and stay empty: the
// composition reaches the unreadable sentence, and the remembered sentence
// after it when this guest's earlier observation dates the reading.
var backupAlarmUnseenClusterWords = backupAlarmWords{
	phraseKnown:            "",
	phraseNeverRecorded:    "",
	phraseUnreadable:       "This guest cannot see the ledger cluster",
	paragraphKnown:         "",
	paragraphKnownDetail:   "",
	paragraphNeverRecorded: "",
	paragraphUnreadable: "This guest cannot reach the ledger cluster (logins and audit trail), " +
		"so it cannot say whether the cluster is healthy.",
	paragraphRemembered: " It last saw the cluster healthy" + backupAlarmKnownFact,
	steps: []string{
		"Confirm every ledger guest is up.",
		"From this guest, confirm the ledger master page answers.",
		"Fix what blocks it; the alarm clears itself once this guest sees the cluster again.",
	},
}

// backupAlarmWordsFor is one fault's vocabulary. A cluster this guest could
// not see takes words of its own, because the mechanism's own vocabulary would
// report the cluster's health, which a guest that heard from no master has not
// established.
func backupAlarmWordsFor(fault backupStalenessMetric) backupAlarmWords {
	if fault.Unknown == backupStalenessClusterUnseen {
		return backupAlarmUnseenClusterWords
	}
	return backupAlarmVocabulary[fault.Name]
}
