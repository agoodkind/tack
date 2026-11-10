// backup_restore_drill_fdb_target.go handles the moment an operator asks the
// FoundationDB restore drill to reach: reading it off the flag, rendering it
// for the engine, and refusing one the engine cannot express or the backup
// cannot reach. Assembling the commands themselves lives beside the restore.

package ops

import (
	"fmt"
	"time"
)

// fdbTimestampArgForm is the only form fdbrestore's --timestamp accepts, quoted
// verbatim from `fdbrestore --help` on foundationdb 7.4.6. It has no
// fractional-second field, so a target is refused rather than rounded when it
// names one.
const fdbTimestampArgForm = "YYYY/MM/DD.HH:MI:SS[+/-]HHMM"

// parseFDBTargetTime reads the operator's --fdb-target-time. Both accepted
// forms carry an explicit UTC offset, so a target time can never mean two
// moments depending on where it was typed. RFC 3339 is the form an operator
// writes; the FoundationDB form is the one they can copy straight out of
// `fdbbackup describe` or `fdbbackup status` output.
//
// A moment fdbrestore cannot express is refused here, at the flag, so the
// operator hears about it before the drill boots a container rather than after.
func parseFDBTargetTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, fdbStatusTimestampLayout} {
		at, parseErr := time.Parse(layout, value)
		if parseErr != nil {
			continue
		}
		if _, err := fdbRestoreTimestampArg(at); err != nil {
			return time.Time{}, err
		}
		return at, nil
	}
	return time.Time{}, fmt.Errorf("restore target time %q is not a time:"+
		" write it as RFC 3339 (2026-08-30T01:07:23Z)"+
		" or in FoundationDB's own form (2026/08/30.01:07:23+0000)", value)
}

// fdbRestoreTimestampArg renders a target as fdbrestore's --timestamp argument,
// and refuses a moment that argument cannot carry.
//
// foundationdb 7.4.6 accepts only whole seconds there. [time.Parse] accepts a
// fraction after the seconds even when the layout omits one, so a sub-second
// target arrives here from both input forms the drill takes. Rendering it would
// drop the fraction and restore the whole second before the moment the operator
// named, so the fraction stops the drill instead.
func fdbRestoreTimestampArg(at time.Time) (string, error) {
	if at.Nanosecond() != 0 {
		return "", fmt.Errorf(
			"restore target %s names a fraction of a second, which FoundationDB's"+
				" --timestamp form %s cannot carry: name a whole second, and note that"+
				" %s is the whole second before the moment given",
			at.UTC().Format(time.RFC3339Nano), fdbTimestampArgForm, formatFDBTime(at))
	}
	return formatFDBTime(at), nil
}

// assertTargetWithinWindow refuses a target the backup cannot reach, naming the
// window so the operator can pick a moment that works. Handing an out-of-window
// time to fdbrestore is exactly what this guard exists for: the restore would
// otherwise produce data from some other moment and report success.
func assertTargetWithinWindow(target time.Time, window fdbRestorableWindow) error {
	if target.Before(window.Min) || target.After(window.Max) {
		return fmt.Errorf(
			"restore target %s is outside the backup's restorable window %s .. %s",
			formatFDBTime(target), formatFDBTime(window.Min), formatFDBTime(window.Max))
	}
	return nil
}

// formatFDBTime renders a moment the way FoundationDB's own tools print and
// accept it, in UTC so a drill's logs and errors read the same everywhere.
func formatFDBTime(at time.Time) string {
	return at.UTC().Format(fdbStatusTimestampLayout)
}
