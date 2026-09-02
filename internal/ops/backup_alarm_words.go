// backup_alarm_words.go is the plain-language form of a backup fault: what has
// stopped, since when, and what to check, in the words an operator reading a
// phone notification acts on. Metric names, thresholds in seconds, and run
// identifiers stay in the journal report; none of them appear here.

package ops

import (
	"fmt"
	"strings"
	"time"

	"goodkind.io/tack/internal/config"
)

// backupAlarmTimeLayout renders a last-success instant for the mail body.
const backupAlarmTimeLayout = "2006-01-02 15:04:05 UTC"

// backupAlarmObjectStoreStandIn replaces the object-store endpoint wherever a
// probe's error text echoes it, so the mail never carries the store's address.
const backupAlarmObjectStoreStandIn = "the object store"

// backupAlarmWords is one mechanism's vocabulary. The known templates take the
// last success time, the age, the threshold, and the detail, in that order; the
// unknown templates take the detail alone.
type backupAlarmWords struct {
	subjectKnown     string
	subjectUnknown   string
	paragraphKnown   string
	paragraphUnknown string
	check            string
}

// backupAlarmVocabulary maps each metric to its words.
var backupAlarmVocabulary = map[string]backupAlarmWords{
	backupStalenessExportName: {
		subjectKnown:   "nightly ledger export last completed %s ago",
		subjectUnknown: "nightly ledger export has no completed run to date",
		paragraphKnown: "The newest complete nightly ledger export finished at %[1]s, %[2]s ago, " +
			"which is older than the %[3]s allowed for an export that runs daily.",
		paragraphUnknown: "No complete nightly ledger export could be dated, so its age is unknown: %[1]s.",
		check: "Check the tack-ledger-export timer on the owner guest and the " +
			"tack-ledger-archive timer on each data guest.",
	},
	backupStalenessRehearsalName: {
		subjectKnown:   "restore rehearsal last passed %s ago",
		subjectUnknown: "restore rehearsal has no recorded pass",
		paragraphKnown: "No restore rehearsal has passed within the %[3]s allowed for a drill that " +
			"runs daily: the last pass was at %[1]s, %[2]s ago.",
		paragraphUnknown: "No restore rehearsal pass has ever been recorded, or the record could not " +
			"be read: %[1]s.",
		check: "Check the tack-backup-restore-drill service journal on the owner guest.",
	},
	backupStalenessReplicationName: {
		subjectKnown:   "ledger cluster has been degraded for %s",
		subjectUnknown: "ledger cluster health is unknown",
		paragraphKnown: "The ledger cluster has reported dead nodes or under-replicated tablets " +
			"continuously since %[1]s, for %[2]s, which is longer than the %[3]s allowed. " +
			"The cluster reports: %[4]s.",
		paragraphUnknown: "The ledger cluster has never been observed healthy, or its health could " +
			"not be read: %[1]s.",
		check: "Check the ledger cluster's node and tablet state.",
	},
	backupStalenessFDBName: {
		subjectKnown:   "FoundationDB backup stopped advancing %s ago",
		subjectUnknown: "FoundationDB backup restorable point is unknown",
		paragraphKnown: "The FoundationDB continuous backup's restorable point is %[1]s, %[2]s ago, " +
			"which is older than the %[3]s allowed. It normally trails the cluster by seconds, " +
			"so writes since that point are not restorable from the object store.",
		paragraphUnknown: "The FoundationDB continuous backup's restorable point could not be read: " +
			"%[1]s. Writes since the last restorable point are not restorable from the object store.",
		check: "Check whether the container tack-fdb-backup-agent-1 is running and can reach " +
			"the object-store endpoint from the configuration.",
	},
}

// backupStalenessAlarmSubject names the guest and the fault. One fault is
// named outright; several are counted, and the body names each.
func backupStalenessAlarmSubject(host string, faults []backupStalenessMetric) string {
	if len(faults) == 1 {
		return "[tack] " + host + ": " + backupAlarmFaultPhrase(faults[0])
	}
	return fmt.Sprintf("[tack] %s: %d backup mechanisms have stopped", host, len(faults))
}

// backupStalenessAlarmBody carries one paragraph per fault, each followed by
// the line naming what to check, and ends by saying the mail does not repeat.
func backupStalenessAlarmBody(cfg *config.Config, host string, faults []backupStalenessMetric) string {
	var body strings.Builder
	for _, fault := range faults {
		body.WriteString(backupAlarmFaultParagraph(cfg, fault))
		body.WriteString("\n")
		body.WriteString(backupAlarmVocabulary[fault.Name].check)
		body.WriteString("\n\n")
	}
	body.WriteString("This mail is sent once when the condition begins and does not repeat. " +
		"Every run's reading is in the tack-backup-staleness journal on " + host + ".\n")
	return body.String()
}

// backupAlarmFaultPhrase is the subject's description of one fault.
func backupAlarmFaultPhrase(fault backupStalenessMetric) string {
	words := backupAlarmVocabulary[fault.Name]
	if !fault.AgeKnown {
		return words.subjectUnknown
	}
	return fmt.Sprintf(words.subjectKnown, backupAlarmClock(fault.Age))
}

// backupAlarmFaultParagraph says what is wrong with one mechanism.
func backupAlarmFaultParagraph(cfg *config.Config, fault backupStalenessMetric) string {
	words := backupAlarmVocabulary[fault.Name]
	detail := backupAlarmDetail(cfg, fault.Detail)
	if !fault.AgeKnown {
		return fmt.Sprintf(words.paragraphUnknown, detail)
	}
	return fmt.Sprintf(words.paragraphKnown,
		fault.At.UTC().Format(backupAlarmTimeLayout),
		backupAlarmClock(fault.Age),
		backupAlarmClock(fault.Threshold),
		detail)
}

// backupAlarmClock renders a duration in hours and minutes, the units a
// reader thinks in: "15h 38m", "36h", "45m".
func backupAlarmClock(d time.Duration) string {
	hours := int(d / time.Hour)
	minutes := int(d%time.Hour) / int(time.Minute)
	if hours > 0 && minutes > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dm", minutes)
}

// backupAlarmDetail prepares a probe's detail for the mail: one line, with
// both object-store credentials redacted and the store's endpoint replaced, so
// an error string that echoes a request URL cannot carry either into a mailbox.
func backupAlarmDetail(cfg *config.Config, detail string) string {
	flattened := redactSecret(cfg, strings.Join(strings.Fields(detail), " "))
	endpoint := cfg.BackupS3Endpoint
	if endpoint == "" {
		return flattened
	}
	authority := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	flattened = strings.ReplaceAll(flattened, endpoint, backupAlarmObjectStoreStandIn)
	return strings.ReplaceAll(flattened, authority, backupAlarmObjectStoreStandIn)
}
