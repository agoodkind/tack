// The FoundationDB restore drill validates the requested target time against
// the engine's timestamp format and the backup's restorable window.

package ops

import (
	"fmt"
	"time"
)

// fdbTimestampArgForm quotes the format from `fdbrestore --help` on
// FoundationDB 7.4.6. The format does not support fractional seconds.
const fdbTimestampArgForm = "YYYY/MM/DD.HH:MI:SS[+/-]HHMM"

// parseFDBTargetTime accepts RFC 3339 or the FoundationDB status timestamp
// format. Both require an explicit UTC offset. Validation rejects unsupported
// precision before the drill starts a container.
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

	return time.Time{}, fmt.Errorf(
		"restore target time %q is not a time:"+
			" write it as RFC 3339 (2026-08-30T01:07:23Z)"+
			" or in FoundationDB's own form (2026/08/30.01:07:23+0000)",
		value,
	)
}

// fdbRestoreTimestampArg renders a whole-second target for --timestamp.
// [time.Parse] accepts fractional seconds even when the layout omits them.
// Rejecting fractions prevents rounding down to an earlier restore time.
func fdbRestoreTimestampArg(at time.Time) (string, error) {
	if at.Nanosecond() != 0 {
		return "", fractionalRestoreTargetError{
			Target: at.UTC().Format(time.RFC3339Nano),
			Format: fdbTimestampArgForm,
			Before: formatFDBTime(at),
		}
	}

	return formatFDBTime(at), nil
}

// assertTargetWithinWindow rejects targets outside the backup's restorable
// window and reports its bounds. Otherwise fdbrestore could restore another
// moment and report success.
func assertTargetWithinWindow(target time.Time, window fdbRestorableWindow) error {
	if target.Before(window.Min) || target.After(window.Max) {
		return fmt.Errorf(
			"restore target %s is outside the backup's restorable window %s .. %s",
			formatFDBTime(target),
			formatFDBTime(window.Min),
			formatFDBTime(window.Max),
		)
	}

	return nil
}

// formatFDBTime renders a FoundationDB timestamp in UTC for consistent logs
// and errors across hosts.
func formatFDBTime(at time.Time) string {
	return at.UTC().Format(fdbStatusTimestampLayout)
}

type fractionalRestoreTargetError struct {
	Target string
	Format string
	Before string
}

func (failure fractionalRestoreTargetError) Error() string {
	return fmt.Sprintf(
		"restore target %s includes a fraction of a second, which FoundationDB's "+
			"--timestamp form %s cannot express: use a whole second, and note that %s is the "+
			"whole second before the moment given",
		failure.Target,
		failure.Format,
		failure.Before,
	)
}
