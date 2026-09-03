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
// last success time, the age, the threshold, and the detail, in that order. An
// unknown age has two vocabularies, chosen by the metric's cause, because a
// success that was never recorded and a record that could not be read support
// different claims; both templates take the detail alone.
type backupAlarmWords struct {
	subjectKnown           string
	subjectNeverRecorded   string
	subjectUnreadable      string
	paragraphKnown         string
	paragraphNeverRecorded string
	paragraphUnreadable    string
	check                  string
}

// backupAlarmVocabulary maps each metric to its words.
var backupAlarmVocabulary = map[string]backupAlarmWords{
	backupStalenessExportName: {
		subjectKnown:         "nightly ledger export last completed %s ago",
		subjectNeverRecorded: "nightly ledger export has no completed run to date",
		subjectUnreadable:    "nightly ledger export could not be dated",
		paragraphKnown: "The newest complete nightly ledger export finished at %[1]s, %[2]s ago, " +
			"which is older than the %[3]s allowed for an export that runs daily.",
		paragraphNeverRecorded: "The object store holds no complete nightly ledger export, " +
			"so there is none to restore the ledger from: %[1]s.",
		paragraphUnreadable: "The newest complete nightly ledger export could not be dated: %[1]s. " +
			"Whether a current export exists is not known until it can be read.",
		check: "Check the tack-ledger-export timer on the owner guest and the " +
			"tack-ledger-archive timer on each data guest.",
	},
	backupStalenessRehearsalName: {
		subjectKnown:         "restore rehearsal last passed %s ago",
		subjectNeverRecorded: "restore rehearsal has no recorded pass",
		subjectUnreadable:    "restore rehearsal's last pass could not be read",
		paragraphKnown: "No restore rehearsal has passed within the %[3]s allowed for a drill that " +
			"runs daily: the last pass was at %[1]s, %[2]s ago.",
		paragraphNeverRecorded: "No restore rehearsal pass has been recorded in the object store: %[1]s.",
		paragraphUnreadable: "The restore rehearsal's last pass could not be read: %[1]s. " +
			"Whether the drill has passed recently is not known until the record can be read.",
		check: "Check the tack-backup-restore-drill service journal on the owner guest.",
	},
	backupStalenessReplicationName: {
		subjectKnown:         "ledger cluster not observed healthy for %s",
		subjectNeverRecorded: "ledger cluster has never been observed healthy",
		subjectUnreadable:    "ledger cluster's last healthy observation could not be read",
		paragraphKnown: "The ledger cluster has not been observed healthy since %[1]s, %[2]s ago, " +
			"which is longer than the %[3]s allowed. The last observation reported: %[4]s.",
		paragraphNeverRecorded: "The ledger cluster has never been observed healthy: %[1]s.",
		paragraphUnreadable:    "The ledger cluster's last healthy observation could not be read: %[1]s.",
		check:                  "Check the ledger cluster's node and tablet state.",
	},
	backupStalenessFDBName: {
		subjectKnown:         "FoundationDB backup stopped advancing %s ago",
		subjectNeverRecorded: "FoundationDB backup has no restorable point",
		subjectUnreadable:    "FoundationDB backup restorable point could not be read",
		paragraphKnown: "The FoundationDB continuous backup's restorable point is %[1]s, %[2]s ago, " +
			"which is older than the %[3]s allowed. It normally trails the cluster by seconds, " +
			"so writes since that point are not restorable from the object store.",
		paragraphNeverRecorded: "The FoundationDB continuous backup reports no restorable point, " +
			"so nothing can be restored from it yet: %[1]s.",
		paragraphUnreadable: "The FoundationDB continuous backup's restorable point could not be read: " +
			"%[1]s. Whether recent writes are restorable is not known until the status can be read.",
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
		if fault.Unknown == backupStalenessNeverRecorded {
			return words.subjectNeverRecorded
		}
		return words.subjectUnreadable
	}
	return fmt.Sprintf(words.subjectKnown, backupAlarmClock(fault.Age))
}

// backupAlarmFaultParagraph says what is wrong with one mechanism. An unknown
// age whose cause is anything but a never-recorded success is worded as
// unreadable, the claim that assumes least.
func backupAlarmFaultParagraph(cfg *config.Config, fault backupStalenessMetric) string {
	words := backupAlarmVocabulary[fault.Name]
	detail := backupAlarmDetail(cfg, fault.Detail)
	if !fault.AgeKnown {
		if fault.Unknown == backupStalenessNeverRecorded {
			return fmt.Sprintf(words.paragraphNeverRecorded, detail)
		}
		return fmt.Sprintf(words.paragraphUnreadable, detail)
	}
	return fmt.Sprintf(words.paragraphKnown,
		fault.At.UTC().Format(backupAlarmTimeLayout),
		backupAlarmClock(fault.Age),
		backupAlarmClock(fault.Threshold),
		detail)
}

// backupAlarmClock renders a duration in the units a reader thinks in: "45m",
// "15h 38m", "36h", and from two days up "8 days" or "9 days 3h", because a
// rehearsal threshold rendered as "192h" makes the reader do the division.
func backupAlarmClock(d time.Duration) string {
	const day = 24 * time.Hour
	if d >= 2*day {
		days := int(d / day)
		remainingHours := int(d%day) / int(time.Hour)
		if remainingHours > 0 {
			return fmt.Sprintf("%d days %dh", days, remainingHours)
		}
		return fmt.Sprintf("%d days", days)
	}
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
