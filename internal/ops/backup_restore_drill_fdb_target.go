// The FoundationDB restore drill parses the operator's target-time flag,
// renders it for the engine, and rejects times that the engine cannot express
// or the backup cannot restore. Separate command construction uses that target.

package ops

import (
	"fmt"
	"time"
)

// fdbTimestampArgForm preserves the --timestamp format from the original
// fdbrestore --help observation on FoundationDB 7.4.6. The format omits
// fractional seconds. The drill rejects fractional targets instead of
// rounding them.
const fdbTimestampArgForm = "YYYY/MM/DD.HH:MI:SS[+/-]HHMM"

// parseFDBTargetTime reads --fdb-target-time. Both accepted formats require
// an explicit UTC offset to identify one moment regardless of the operator's
// location. Operators can write RFC 3339 or copy the FoundationDB format from
// fdbbackup describe or fdbbackup status output.
//
// Validation rejects a time that fdbrestore cannot express before the drill
// starts a container.
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

// fdbRestoreTimestampArg renders the --timestamp argument and rejects a
// target that the argument cannot express.
//
// FoundationDB 7.4.6 accepts whole seconds. [time.Parse] accepts a fraction
// even when the layout omits it, including both drill input formats. Rendering
// would discard the fraction and restore the preceding whole second. The
// drill rejects the fraction instead.
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

// assertTargetWithinWindow rejects a target outside the backup's restorable
// window and reports the window for selecting another time. Without that
// guard, fdbrestore could restore a different moment and report success.
func assertTargetWithinWindow(target time.Time, window fdbRestorableWindow) error {
	if target.Before(window.Min) || target.After(window.Max) {
		return fmt.Errorf(
			"restore target %s is outside the backup's restorable window %s .. %s",
			formatFDBTime(target), formatFDBTime(window.Min), formatFDBTime(window.Max))
	}
	return nil
}

// formatFDBTime renders the format that FoundationDB tools print and accept.
// It uses UTC to keep drill logs and errors consistent across locations.
func formatFDBTime(at time.Time) string {
	return at.UTC().Format(fdbStatusTimestampLayout)
}
