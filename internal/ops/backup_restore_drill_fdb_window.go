// The drill parses restorable time and version ranges from fdbbackup
// describe output. These helpers parse text without executing the command.

package ops

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// fdbRestorableWindow records the earliest and latest restorable times for a
// FoundationDB continuous backup. Backup work extends the latest time, and
// expiration advances the earliest time. Targeted restore selection reads a
// fresh window.
type fdbRestorableWindow struct {
	Min time.Time `json:"min"`
	Max time.Time `json:"max"`
}

const (
	// fdbDescribeMinLabel and fdbDescribeMaxLabel identify the two window lines
	// in fdbbackup describe output. The original observation used format strings
	// from the FoundationDB 7.4.6 binary, including "MinRestorableVersion:    %s".
	// The binary prints these example lines with --version-timestamps:
	//
	// 	MinRestorableVersion:    100720665 (2026/08/30.01:07:23+0000)
	// 	MaxRestorableVersion:    100731044 (2026/08/30.01:10:03+0000)
	//
	// The parenthesized timestamps require that flag.
	fdbDescribeMinLabel = "MinRestorableVersion:"
	fdbDescribeMaxLabel = "MaxRestorableVersion:"
	// fdbDescribeRestorableTrue identifies the affirmative restorable verdict.
	// The parser rejects output that does not report a restorable backup.
	fdbDescribeRestorableTrue = "Restorable: true"
)

// fdbRestorableWindowFromDescribe parses the time range from fdbbackup
// describe --version-timestamps output. It rejects output that does not
// establish restorability or a readable, ordered window.
//
// An unreadable window must not cause a restore of the latest data in place
// of the requested time. Selection may inspect another backup, but must not
// silently substitute another time.
//
// Callers must not log raw describe output because its destination URL
// includes blobstore credentials. Parser errors quote only window lines;
// the expected window lines do not include credentials.
func fdbRestorableWindowFromDescribe(describe string) (fdbRestorableWindow, error) {
	if !describeReportsRestorable(describe) {
		return fdbRestorableWindow{}, errors.New(
			"fdbbackup describe does not report the backup as restorable",
		)
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
			formatFDBTime(minTime),
			formatFDBTime(maxTime),
		)
	}

	return fdbRestorableWindow{Min: minTime, Max: maxTime}, nil
}

// describeReportsRestorable checks for a complete Restorable: true line
// after trimming surrounding whitespace. An echoed destination URL that
// contains those words does not substitute for the verdict.
func describeReportsRestorable(describe string) bool {
	for line := range strings.SplitSeq(describe, "\n") {
		if strings.TrimSpace(line) == fdbDescribeRestorableTrue {
			return true
		}
	}

	return false
}

// fdbRestorableVersions records the earliest and latest restorable versions
// from fdbbackup describe output without --version-timestamps.
type fdbRestorableVersions struct {
	MinVersion int64 `json:"min_version"`
	MaxVersion int64 `json:"max_version"`
}

// fdbRestorableVersionsFromDescribe parses version bounds from fdbbackup
// describe output without --version-timestamps. TACK-609 recorded a failure
// after hundreds of snapshots: that flag converts every snapshot version
// within one transaction, and FoundationDB aborts the transaction.
//
// Callers must not log raw describe output because its destination URL
// includes blobstore credentials.
func fdbRestorableVersionsFromDescribe(describe string) (fdbRestorableVersions, error) {
	if !describeReportsRestorable(describe) {
		return fdbRestorableVersions{}, errors.New(
			"fdbbackup describe does not report the backup as restorable",
		)
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
			"fdbbackup describe reports an inverted restorable window %d .. %d",
			minVersion,
			maxVersion,
		)
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
			return 0, fmt.Errorf(
				"fdbbackup describe line %q does not carry a positive version",
				strings.TrimSpace(line),
			)
		}

		return version, nil
	}

	return 0, fmt.Errorf("fdbbackup describe has no %s line", label)
}

// fdbDescribeTimestamp parses the parenthesized timestamp on a describe line.
// Without --version-timestamps, fdbbackup prints "(unknown)" or a relative
// "(maxLogEnd -0.53 days)". Neither value parses as a timestamp, and the helper
// returns an error rather than accepting an unchecked restore target.
func fdbDescribeTimestamp(describe string, label string) (time.Time, error) {
	for line := range strings.SplitSeq(describe, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, label) {
			continue
		}

		openIndex := strings.Index(trimmed, "(")
		closeIndex := strings.LastIndex(trimmed, ")")
		if openIndex < 0 || closeIndex < openIndex {
			return time.Time{}, fmt.Errorf(
				"fdbbackup describe line %q carries no timestamp;"+
					" describe needs --version-timestamps and a source cluster file",
				trimmed,
			)
		}

		stamp := trimmed[openIndex+1 : closeIndex]
		timestamp, err := time.Parse(fdbStatusTimestampLayout, stamp)
		if err != nil {
			return time.Time{}, fmt.Errorf(
				"fdbbackup describe line %q carries no timestamp;"+
					" describe needs --version-timestamps and a source cluster file: %w",
				trimmed,
				err,
			)
		}

		return timestamp, nil
	}

	return time.Time{}, fmt.Errorf("fdbbackup describe has no %s line", label)
}
