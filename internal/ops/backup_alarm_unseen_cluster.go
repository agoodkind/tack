// A guest without a ledger master response cannot establish cluster health.
// Its alarm must report the observation failure rather than claim that the
// cluster is unhealthy. TACK-529 documented this distinction. Other faults
// continue to use the mechanism vocabulary.

package ops

// backupAlarmUnseenClusterWords supplies text for a cluster that the guest
// cannot observe. This cause does not include a fresh success; the known and
// never-recorded templates remain empty. Composition uses the unreadable
// sentence and appends remembered observation text when this guest has an
// earlier cluster reading.
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

// backupAlarmWordsFor selects a fault's vocabulary. An unseen cluster uses
// observation-failure wording instead of the mechanism's health claims, which
// require an actual master response.
func backupAlarmWordsFor(fault backupStalenessMetric) backupAlarmWords {
	if fault.Unknown == backupStalenessClusterUnseen {
		return backupAlarmUnseenClusterWords
	}

	return backupAlarmVocabulary[fault.Name]
}
