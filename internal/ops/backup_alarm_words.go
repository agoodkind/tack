// backup_alarm_words.go composes the backup alarm mail an operator reads on a
// phone: which environment it is from, what happened in plain words, and what
// to do about it. Every time is UTC and says so where it is printed. Each
// mechanism's own words are in backup_alarm_vocabulary.go.

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

// backupAlarmUnnamedEnvironment labels the mail when no environment name is
// configured, so a missing label is visible rather than silently absent.
const backupAlarmUnnamedEnvironment = "unnamed environment"

// backupAlarmProductionLabel is the one environment whose mail says it is
// production; every other label says it is not.
const backupAlarmProductionLabel = "Production"

// backupAlarmObjectStoreStandIn replaces the object-store endpoint wherever a
// probe's error text echoes it, so the mail never carries the store's address.
const backupAlarmObjectStoreStandIn = "the object store"

// backupAlarmScene is where the mail is from: the environment label it carries
// and the guest whose check found the fault.
type backupAlarmScene struct {
	Environment string
	Host        string
}

// backupAlarmEnvironmentLabel is the configured environment name, or the
// unnamed stand-in when none is set.
func backupAlarmEnvironmentLabel(configured string) string {
	if strings.TrimSpace(configured) == "" {
		return backupAlarmUnnamedEnvironment
	}
	return strings.TrimSpace(configured)
}

// backupStalenessAlarmSubject labels the environment and names the fault. One
// fault is named outright; several are counted, and the body names each.
func backupStalenessAlarmSubject(scene backupAlarmScene, faults []backupStalenessMetric) string {
	prefix := "[Tack " + scene.Environment + "] "
	if len(faults) == 1 {
		return prefix + backupAlarmFaultPhrase(faults[0])
	}
	return prefix + strconv.Itoa(len(faults)) + " backup problems need attention"
}

// backupStalenessAlarmBody opens with where the mail is from, says what
// happened one paragraph per fault, lists what to do, and ends by saying the
// mail does not repeat. With several faults each list sits under the fault's
// plain name.
func backupStalenessAlarmBody(cfg *config.Config, scene backupAlarmScene, faults []backupStalenessMetric) string {
	var body strings.Builder
	body.WriteString(backupAlarmOpening(scene))
	body.WriteString("\n\nWHAT HAPPENED\n")
	for i, fault := range faults {
		if i > 0 {
			body.WriteString("\n")
		}
		body.WriteString(backupAlarmFaultParagraph(cfg, fault))
		body.WriteString("\n")
	}
	body.WriteString("\nWHAT TO DO\n")
	for i, fault := range faults {
		words := backupAlarmVocabulary[fault.Name]
		if len(faults) > 1 {
			if i > 0 {
				body.WriteString("\n")
			}
			body.WriteString(words.name + "\n")
		}
		for n, step := range words.steps {
			body.WriteString(strconv.Itoa(n+1) + ". " + step + "\n")
		}
	}
	body.WriteString("\nThis mail is sent once per problem. " +
		"Every check's reading is in the tack-backup-staleness journal on " + scene.Host + ".")
	return body.String()
}

// backupAlarmOpening is the first sentence: the environment, the guest, and
// whether this is production, said outright either way.
func backupAlarmOpening(scene backupAlarmScene) string {
	if strings.EqualFold(scene.Environment, backupAlarmProductionLabel) {
		return "This is the production environment, guest " + scene.Host + "."
	}
	where := "the " + scene.Environment + " environment"
	if scene.Environment == backupAlarmUnnamedEnvironment {
		where = "an unnamed environment"
	}
	return "This is " + where + ", guest " + scene.Host + ", not production."
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
