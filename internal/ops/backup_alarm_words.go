// Backup alarm email identifies the guest, explains each fault, and provides
// recovery commands. Times explicitly use UTC. Footer composition adds run,
// host, and send-time details separately.

package ops

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"goodkind.io/tack/internal/config"
)

// backupAlarmTimeLayout prints the time zone beside the clock value.
const backupAlarmTimeLayout = "3:04 PM MST on Jan 2, 2006"

// backupAlarmObjectStoreStandIn replaces endpoint addresses in email details.
const backupAlarmObjectStoreStandIn = "the object store"

const (
	// backupAlarmStoreNoun explains the object store at its first mention.
	backupAlarmStoreNoun = "The object store (which stores the backups) did not answer"
	// backupAlarmStoreStep checks store availability before mechanism recovery.
	backupAlarmStoreStep = " Confirm the object store guest is running before the steps below."
)

// backupStalenessAlarmSubject identifies the guest and one fault or counts
// multiple faults that the body explains individually.
func backupStalenessAlarmSubject(host string, faults []backupStalenessMetric) string {
	prefix := "[" + host + "] "
	if len(faults) == 1 {
		return prefix + backupAlarmFaultPhrase(faults[0])
	}

	return prefix + strconv.Itoa(len(faults)) + " backup problems"
}

// backupStalenessAlarmBody formats each fault and its recovery steps. Multiple
// faults add individual headings; a single fault uses its subject. Shared store
// failure appears once in an opening paragraph rather than repeating per fault.
func backupStalenessAlarmBody(cfg *config.Config, faults []backupStalenessMetric) string {
	var body strings.Builder
	if store, unreachable := backupAlarmObjectStoreParagraph(faults); unreachable {
		body.WriteString(store + "\n\n")
	}

	for i, fault := range faults {
		if i > 0 {
			body.WriteString("\n\n")
		}

		if len(faults) > 1 {
			body.WriteString(backupAlarmFaultPhrase(fault) + "\n")
			body.WriteString(backupAlarmFaultParagraph(cfg, fault) + "\n")
		} else {
			body.WriteString(backupAlarmFaultParagraph(cfg, fault) + "\n\n")
		}

		body.WriteString(backupAlarmSteps(fault))
	}

	return body.String()
}

// backupAlarmObjectStoreParagraph reports an unanswered store request and the
// newest prior observation among affected metrics. Without a prior read, the
// paragraph does not claim a last-read time.
func backupAlarmObjectStoreParagraph(faults []backupStalenessMetric) (string, bool) {
	unreachable := false
	var lastRead time.Time

	for _, fault := range faults {
		if fault.Unknown != backupStalenessStoreUnreachable {
			continue
		}

		unreachable = true
		if fault.LastReadAt.After(lastRead) {
			lastRead = fault.LastReadAt
		}
	}

	if !unreachable {
		return "", false
	}

	if lastRead.IsZero() {
		return backupAlarmStoreNoun + " this check." + backupAlarmStoreStep, true
	}

	return backupAlarmStoreNoun + "; this guest last read it at " +
		lastRead.UTC().Format(backupAlarmTimeLayout) + "." + backupAlarmStoreStep, true
}

// backupAlarmSteps numbers one mechanism's recovery steps, one per line.
func backupAlarmSteps(fault backupStalenessMetric) string {
	words := backupAlarmWordsFor(fault)

	lines := make([]string, 0, len(words.steps))
	for n, step := range words.steps {
		lines = append(lines, strconv.Itoa(n+1)+". "+step)
	}

	return strings.Join(lines, "\n")
}

// backupAlarmFaultPhrase selects a fault's subject or body heading.
func backupAlarmFaultPhrase(fault backupStalenessMetric) string {
	words := backupAlarmWordsFor(fault)
	if fault.Unknown == backupStalenessNeverRecorded {
		return words.phraseNeverRecorded
	}

	if fault.Unknown != backupStalenessAgeKnown {
		return words.phraseUnreadable
	}

	return fmt.Sprintf(words.phraseKnown, backupAlarmClock(fault.Age))
}

// backupAlarmFaultParagraph distinguishes absent successes from unreadable
// readings without exposing raw client errors. Historical readings add the
// previous success time and age. Only mechanisms with paragraphKnownDetail
// include the diagnostic detail sentence.
func backupAlarmFaultParagraph(cfg *config.Config, fault backupStalenessMetric) string {
	words := backupAlarmWordsFor(fault)
	if fault.Unknown == backupStalenessNeverRecorded {
		return fmt.Sprintf(words.paragraphNeverRecorded, backupAlarmDetail(cfg, fault.Detail))
	}

	if fault.Unknown != backupStalenessAgeKnown {
		if !fault.AgeKnown {
			return words.paragraphUnreadable
		}

		return words.paragraphUnreadable + fmt.Sprintf(
			words.paragraphRemembered,
			fault.At.UTC().Format(backupAlarmTimeLayout),
			backupAlarmClock(fault.Age),
			backupAlarmClock(fault.Threshold),
		)
	}

	paragraph := fmt.Sprintf(
		words.paragraphKnown,
		fault.At.UTC().Format(backupAlarmTimeLayout),
		backupAlarmClock(fault.Age),
		backupAlarmClock(fault.Threshold),
	)
	if words.paragraphKnownDetail == "" {
		return paragraph
	}

	return paragraph + fmt.Sprintf(words.paragraphKnownDetail, backupAlarmDetail(cfg, fault.Detail))
}

// backupAlarmClock prints minutes and hours, or days and remaining hours for
// durations of at least two days.
func backupAlarmClock(d time.Duration) string {
	const day = 24 * time.Hour
	if d >= 2*day {
		days := backupAlarmCount(int(d/day), "day")
		if remainingHours := int((d % day) / time.Hour); remainingHours > 0 {
			return days + " " + backupAlarmCount(remainingHours, "hour")
		}

		return days
	}

	hours := int(d / time.Hour)
	minutes := int((d % time.Hour) / time.Minute)
	if hours > 0 && minutes > 0 {
		return backupAlarmCount(hours, "hour") + " " + backupAlarmCount(minutes, "minute")
	}

	if hours > 0 {
		return backupAlarmCount(hours, "hour")
	}

	return backupAlarmCount(minutes, "minute")
}

// backupAlarmCount prints a count and its singular or plural unit.
func backupAlarmCount(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}

	return strconv.Itoa(n) + " " + unit + "s"
}

// backupAlarmDetail flattens details, redacts both S3 credentials, and replaces
// the endpoint address before including text in email.
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
