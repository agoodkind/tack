// backup_alarm_words.go composes the backup alarm mail an operator reads on a
// phone: the guest in the subject, what happened in one sentence per fault,
// and the steps that fix it. Every time is UTC and says so where it is
// printed. The host and the send time appear only in the footer the mailer
// library appends. Each mechanism's own words are in
// backup_alarm_vocabulary.go.

package ops

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"goodkind.io/tack/internal/config"
)

// backupAlarmTimeLayout renders a last-good instant the way a reader says it,
// with the zone beside the clock so no time in the mail is mistaken for local.
const backupAlarmTimeLayout = "3:04 PM MST on Jan 2, 2006"

// backupAlarmObjectStoreStandIn replaces the object-store endpoint wherever a
// probe's error text echoes it, so the mail never carries the store's address.
const backupAlarmObjectStoreStandIn = "the object store"

// backupStalenessAlarmSubject names the guest and the fault. One fault is
// named outright; several are counted, and the body names each.
func backupStalenessAlarmSubject(host string, faults []backupStalenessMetric) string {
	prefix := "[" + host + "] "
	if len(faults) == 1 {
		return prefix + backupAlarmFaultPhrase(faults[0])
	}
	return prefix + strconv.Itoa(len(faults)) + " backup problems"
}

// backupStalenessAlarmBody is one sentence of fact and the steps for each
// fault. A single fault needs no name, because the subject carries it; with
// several, each block opens with the fault's phrase and a blank line separates
// the blocks.
func backupStalenessAlarmBody(cfg *config.Config, faults []backupStalenessMetric) string {
	var body strings.Builder
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

// backupAlarmSteps numbers one mechanism's steps, one per line.
func backupAlarmSteps(fault backupStalenessMetric) string {
	words := backupAlarmVocabulary[fault.Name]
	lines := make([]string, 0, len(words.steps))
	for n, step := range words.steps {
		lines = append(lines, strconv.Itoa(n+1)+". "+step)
	}
	return strings.Join(lines, "\n")
}

// backupAlarmFaultPhrase names one fault in the subject and, with several
// faults, at the head of its block.
func backupAlarmFaultPhrase(fault backupStalenessMetric) string {
	words := backupAlarmVocabulary[fault.Name]
	if !fault.AgeKnown {
		if fault.Unknown == backupStalenessNeverRecorded {
			return words.phraseNeverRecorded
		}
		return words.phraseUnreadable
	}
	return fmt.Sprintf(words.phraseKnown, backupAlarmClock(fault.Age))
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

// backupAlarmClock renders a duration in the words a reader thinks in:
// "45 minutes", "15 hours 38 minutes", "36 hours", and from two days up
// "8 days" or "9 days 3 hours", because a rehearsal limit rendered as 192
// hours makes the reader do the division.
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

// backupAlarmCount is a number with its unit, singular for one.
func backupAlarmCount(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
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
