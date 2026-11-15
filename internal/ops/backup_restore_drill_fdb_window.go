package ops

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

var (
	errFDBVersion      = errors.New("fdbbackup describe line does not contain a positive version")
	errFDBMissingBound = errors.New("fdbbackup describe does not include a bound line")
	errFDBTimestamp    = errors.New("fdbbackup describe line does not contain a valid timestamp")
)

// fdbRestorableWindow is the time range for a continuous backup restore.
// Read it before each restore because writes and expiration change its bounds.
type fdbRestorableWindow struct {
	Min time.Time `json:"min"`
	Max time.Time `json:"max"`
}

const (
	// fdbbackup 7.4.6 prints these bounds with --version-timestamps:
	//   MinRestorableVersion:    100720665 (2026/08/30.01:07:23+0000)
	//   MaxRestorableVersion:    100731044 (2026/08/30.01:10:03+0000)
	fdbDescribeMinLabel = "MinRestorableVersion:"
	fdbDescribeMaxLabel = "MaxRestorableVersion:"
	// fdbDescribeRestorableTrue confirms that the backup supports a restore.
	fdbDescribeRestorableTrue = "Restorable: true"
)

// fdbRestorableWindowFromDescribe reads the window from
// fdbbackup describe --version-timestamps. Invalid or missing bounds must stop
// the restore rather than restore the latest data, not the requested time.
// Do not log raw output: it includes the URL and blobstore credentials.
// Errors quote only window lines, which do not include credentials.
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

// describeReportsRestorable matches the complete Restorable: true line.
// It does not accept matching text within the destination URL.
func describeReportsRestorable(describe string) bool {
	for line := range strings.SplitSeq(describe, "\n") {
		if strings.TrimSpace(line) == fdbDescribeRestorableTrue {
			return true
		}
	}

	return false
}

// fdbRestorableVersions is the version range from fdbbackup describe without
// --version-timestamps.
type fdbRestorableVersions struct {
	MinVersion int64 `json:"min_version"`
	MaxVersion int64 `json:"max_version"`
}

// fdbRestorableVersionsFromDescribe reads version bounds without
// --version-timestamps. That flag converts every snapshot version in one
// transaction, which FoundationDB aborts for backups with hundreds of snapshots
// (TACK-609). Do not log raw output: it includes blobstore credentials.
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

// fdbDescribeVersion reads the integer before the parenthesis on a line.
// Without --version-timestamps, fdbbackup prints a relative value such as
// "(maxLogEnd -0.53 days)" or "(unknown)" after the version.
func fdbDescribeVersion(describe string, label string) (int64, error) {
	for line := range strings.SplitSeq(describe, "\n") {
		value, found := strings.CutPrefix(strings.TrimSpace(line), label)
		if !found {
			continue
		}

		rawVersion, _, _ := strings.Cut(value, "(")
		version, err := strconv.ParseInt(strings.TrimSpace(rawVersion), 10, 64)
		if err != nil || version <= 0 {
			slog.Error("backup.fdb.version_invalid", slog.Any("err", errFDBVersion))

			return 0, fmt.Errorf("%w: %q", errFDBVersion, strings.TrimSpace(line))
		}

		return version, nil
	}

	slog.Error(
		"backup.fdb.bound_missing",
		slog.Any("err", errFDBMissingBound),
		slog.String("label", label),
	)

	return 0, fmt.Errorf(
		"fdbbackup describe does not include a %s line: %w",
		label,
		errFDBMissingBound,
	)
}

// fdbDescribeTimestamp reads a parenthesized timestamp. It rejects relative
// values and "(unknown)" from output without --version-timestamps to prevent
// a restore with an unchecked target.
func fdbDescribeTimestamp(describe string, label string) (time.Time, error) {
	for line := range strings.SplitSeq(describe, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, label) {
			continue
		}

		openIndex := strings.Index(trimmed, "(")
		closeIndex := strings.LastIndex(trimmed, ")")
		if openIndex < 0 || closeIndex < openIndex {
			slog.Error("backup.fdb.timestamp_invalid", slog.Any("err", errFDBTimestamp))

			return time.Time{}, fmt.Errorf(
				"%w: %q; describe needs --version-timestamps and a source cluster file",
				errFDBTimestamp,
				trimmed,
			)
		}

		stamp := trimmed[openIndex+1 : closeIndex]
		at, err := time.Parse(fdbStatusTimestampLayout, stamp)
		if err != nil {
			return time.Time{}, fmt.Errorf(
				"fdbbackup describe line %q does not contain a valid timestamp;"+
					" describe needs --version-timestamps and a source cluster file: %w",
				trimmed,
				err,
			)
		}

		return at, nil
	}

	slog.Error(
		"backup.fdb.bound_missing",
		slog.Any("err", errFDBMissingBound),
		slog.String("label", label),
	)

	return time.Time{}, fmt.Errorf(
		"fdbbackup describe does not include a %s line: %w",
		label,
		errFDBMissingBound,
	)
}
