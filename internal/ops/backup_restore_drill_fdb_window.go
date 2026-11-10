// backup_restore_drill_fdb_window.go reads the span of moments a FoundationDB
// continuous backup can be restored to out of `fdbbackup describe` output. It
// is pure text handling; the describe call itself lives beside the restore.

package ops

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// fdbRestorableWindow is the span of moments a FoundationDB continuous backup
// can be restored to. It advances while the backup streams and its old end
// moves forward as data expires, so it is read fresh for every restore.
type fdbRestorableWindow struct {
	Min time.Time `json:"min"`
	Max time.Time `json:"max"`
}

const (
	// fdbDescribeMinLabel and fdbDescribeMaxLabel are the lines `fdbbackup
	// describe` prints for the two ends of the restorable window. Captured
	// from the format strings compiled into foundationdb 7.4.6's fdbbackup
	// binary ("MinRestorableVersion:    %s"), which render as:
	//   MinRestorableVersion:    100720665 (2026/08/30.01:07:23+0000)
	//   MaxRestorableVersion:    100731044 (2026/08/30.01:10:03+0000)
	// The parenthesized timestamp appears only with --version-timestamps.
	fdbDescribeMinLabel = "MinRestorableVersion:"
	fdbDescribeMaxLabel = "MaxRestorableVersion:"
	// fdbDescribeRestorableTrue is the line that vouches for the backup being
	// restorable at all. A describe that reports false has no window.
	fdbDescribeRestorableTrue = "Restorable: true"
)

// fdbRestorableWindowFromDescribe reads the restorable window out of
// `fdbbackup describe --version-timestamps` output.
//
// Every failure here is loud on purpose. A describe that omits the timestamps,
// denies restorability, or names a window this code cannot read has to stop the
// restore: falling through would hand the operator the latest data instead of
// the moment they asked for, which is the failure this capability exists to
// remove.
//
// Callers must not log the raw describe output, which echoes the destination
// URL and its blobstore credentials. The errors below quote only the window
// lines, which carry no credentials.
func fdbRestorableWindowFromDescribe(describe string) (fdbRestorableWindow, error) {
	if !describeReportsRestorable(describe) {
		return fdbRestorableWindow{}, errors.New(
			"fdbbackup describe does not report the backup as restorable")
	}
	minTime, err := fdbDescribeTimestamp(describe, fdbDescribeMinLabel)
	if err != nil {
		return fdbRestorableWindow{}, err
	}
	maxTime, err := fdbDescribeTimestamp(describe, fdbDescribeMaxLabel)
	if err != nil {
		return fdbRestorableWindow{}, err
	}
	if maxTime.Before(minTime) {
		return fdbRestorableWindow{}, fmt.Errorf(
			"fdbbackup describe reports an inverted restorable window %s .. %s",
			formatFDBTime(minTime), formatFDBTime(maxTime))
	}
	return fdbRestorableWindow{Min: minTime, Max: maxTime}, nil
}

// describeReportsRestorable reports whether the describe output vouches for a
// restorable backup. It matches a whole line, so the destination URL echoed
// above the verdict can never stand in for the verdict itself.
func describeReportsRestorable(describe string) bool {
	for line := range strings.SplitSeq(describe, "\n") {
		if strings.TrimSpace(line) == fdbDescribeRestorableTrue {
			return true
		}
	}
	return false
}

// fdbRestorableVersions is the span of versions a FoundationDB continuous
// backup can be restored to, as `fdbbackup describe` prints it without
// --version-timestamps.
type fdbRestorableVersions struct {
	MinVersion int64 `json:"min_version"`
	MaxVersion int64 `json:"max_version"`
}

// fdbRestorableVersionsFromDescribe reads the restorable window as versions
// out of `fdbbackup describe` output made without --version-timestamps. That
// flag converts every snapshot version to a time inside one transaction, and
// FoundationDB aborts that transaction after a backup writes hundreds of
// snapshots (TACK-609). Callers must not log the raw describe output, which
// echoes the destination URL and its blobstore credentials.
func fdbRestorableVersionsFromDescribe(describe string) (fdbRestorableVersions, error) {
	if !describeReportsRestorable(describe) {
		return fdbRestorableVersions{}, errors.New(
			"fdbbackup describe does not report the backup as restorable")
	}
	minVersion, err := fdbDescribeVersion(describe, fdbDescribeMinLabel)
	if err != nil {
		return fdbRestorableVersions{}, err
	}
	maxVersion, err := fdbDescribeVersion(describe, fdbDescribeMaxLabel)
	if err != nil {
		return fdbRestorableVersions{}, err
	}
	if maxVersion < minVersion {
		return fdbRestorableVersions{}, fmt.Errorf(
			"fdbbackup describe reports an inverted restorable window %d .. %d", minVersion, maxVersion)
	}
	return fdbRestorableVersions{MinVersion: minVersion, MaxVersion: maxVersion}, nil
}

// fdbDescribeVersion reads the integer version before the parenthesis on one
// describe line. Without --version-timestamps, fdbbackup prints a relative
// "(maxLogEnd -0.53 days)" or "(unknown)" in that position.
func fdbDescribeVersion(describe string, label string) (int64, error) {
	for line := range strings.SplitSeq(describe, "\n") {
		value, found := strings.CutPrefix(strings.TrimSpace(line), label)
		if !found {
			continue
		}
		rawVersion, _, _ := strings.Cut(value, "(")
		version, err := strconv.ParseInt(strings.TrimSpace(rawVersion), 10, 64)
		if err != nil || version <= 0 {
			return 0, fmt.Errorf("fdbbackup describe line %q does not carry a positive version", strings.TrimSpace(line))
		}
		return version, nil
	}
	return 0, fmt.Errorf("fdbbackup describe has no %s line", label)
}

// fdbDescribeTimestamp pulls the parenthesized timestamp off one describe line.
// Run without --version-timestamps, fdbbackup prints "(unknown)" or a relative
// "(maxLogEnd -0.53 days)" in that position; neither parses, so a describe made
// the wrong way fails here instead of passing an unchecked target through to
// the restore.
func fdbDescribeTimestamp(describe string, label string) (time.Time, error) {
	for line := range strings.SplitSeq(describe, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, label) {
			continue
		}
		openIndex := strings.Index(trimmed, "(")
		closeIndex := strings.LastIndex(trimmed, ")")
		if openIndex < 0 || closeIndex < openIndex {
			return time.Time{}, fmt.Errorf("fdbbackup describe line %q carries no timestamp;"+
				" describe needs --version-timestamps and a source cluster file", trimmed)
		}
		stamp := trimmed[openIndex+1 : closeIndex]
		at, err := time.Parse(fdbStatusTimestampLayout, stamp)
		if err != nil {
			return time.Time{}, fmt.Errorf("fdbbackup describe line %q carries no timestamp;"+
				" describe needs --version-timestamps and a source cluster file: %w", trimmed, err)
		}
		return at, nil
	}
	return time.Time{}, fmt.Errorf("fdbbackup describe has no %s line", label)
}
