// Backup alarm messages identify the guest in the subject and give one
// sentence per fault followed by recovery steps. The layout supports reading
// the message on a phone. Every printed time includes UTC.
//
// The mailer appends the footer with the run, host, and send time. Each backup
// mechanism supplies its own vocabulary.

package ops

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"goodkind.io/tack/internal/config"
)

// backupAlarmTimeLayout formats the last successful time with the time zone
// beside the clock. Alarm messages show UTC rather than an ambiguous local
// time.
const backupAlarmTimeLayout = "3:04 PM MST on Jan 2, 2006"

// backupAlarmObjectStoreStandIn replaces object-store endpoints in probe
// errors. Alarm messages must not expose the store's address.
const backupAlarmObjectStoreStandIn = "the object store"

const (
	// backupAlarmStoreNoun starts the sentence about an object store that did not
	// answer. It explains the store at its first mention.
	backupAlarmStoreNoun = "The object store (where the backups are kept) did not answer"
	// backupAlarmStoreStep asks the reader to check the object store before
	// following recovery steps for mechanisms that the store could not date.
	backupAlarmStoreStep = " Confirm the object store guest is running before the steps below."
)

// backupStalenessAlarmSubject identifies the guest and the fault. It states
// one fault directly or counts several faults that the body lists separately.
func backupStalenessAlarmSubject(host string, faults []backupStalenessMetric) string {
	prefix := "[" + host + "] "
	if len(faults) == 1 {
		return prefix + backupAlarmFaultPhrase(faults[0])
	}

	return prefix + strconv.Itoa(len(faults)) + " backup problems"
}

// backupStalenessAlarmBody writes a factual sentence and recovery steps for
// each fault. It does not repeat a single fault's phrase from the subject.
// For several faults, each block starts with its fault's phrase, and blank
// lines separate the blocks.
//
// If any fault reports that the object store did not answer, the body starts
// with one paragraph about the store. The fault sentences do not repeat that
// failure.
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

// backupAlarmObjectStoreParagraph reports that the object store did not
// answer if any fault has that cause. It includes the newest previous store
// read among those faults. If the guest has never read the store, the
// sentence does not state a previous read time.
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

// backupAlarmSteps numbers one mechanism's steps, one per line.
func backupAlarmSteps(fault backupStalenessMetric) string {
	words := backupAlarmWordsFor(fault)

	lines := make([]string, 0, len(words.steps))
	for n, step := range words.steps {
		lines = append(lines, strconv.Itoa(n+1)+". "+step)
	}

	return strings.Join(lines, "\n")
}

// backupAlarmFaultPhrase returns a fault's phrase for the subject and for
// the start of its block when the message contains several faults.
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

// backupAlarmFaultParagraph explains one mechanism's fault. A cause other
// than a known age or a success never recorded uses the unreadable wording
// without the failure's error text. When the guest has a dated previous
// reading, the sentence includes that reading's last successful time.
//
// A known-age paragraph includes detail only when paragraphKnownDetail
// supplies a sentence for it. An earlier composer supplied detail to every
// mechanism and silently omitted it from three of the four messages.
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

// backupAlarmClock formats durations in familiar units. Examples include
// "45 minutes", "15 hours 38 minutes", and "36 hours". From two days onward,
// it uses days, as in "8 days" or "9 days 3 hours". A rehearsal limit of
// 192 hours otherwise requires the reader to convert hours to days.
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

// backupAlarmCount returns a number and its unit. It uses the singular unit
// for one.
func backupAlarmCount(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}

	return strconv.Itoa(n) + " " + unit + "s"
}

// backupAlarmDetail formats a probe's detail as one line for the message.
// It redacts both object-store credentials and replaces the store endpoint,
// including when an error repeats them in a request URL.
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
