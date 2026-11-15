package ops

import (
	"fmt"
	"strconv"
	"strings"
)

type fdbVersionBound struct {
	label string
	value int64
}

// UnmarshalText decodes the first matching FoundationDB version bound.
//
// It accepts a positive decimal version before the first parenthesis.
// Invalid or missing bounds return an error without emitting logs.
//
// For example:
//
//	bound := fdbVersionBound{label: fdbDescribeMinLabel}
//	err := bound.UnmarshalText([]byte("MinRestorableVersion: 123"))
func (bound *fdbVersionBound) UnmarshalText(text []byte) error {
	for line := range strings.SplitSeq(string(text), "\n") {
		value, found := strings.CutPrefix(strings.TrimSpace(line), bound.label)
		if !found {
			continue
		}

		rawVersion, _, _ := strings.Cut(value, "(")
		version, err := strconv.ParseInt(strings.TrimSpace(rawVersion), 10, 64)
		if err != nil || version <= 0 {
			return fmt.Errorf("%w: %q", errFDBVersion, strings.TrimSpace(line))
		}

		bound.value = version

		return nil
	}

	return fmt.Errorf(
		"fdbbackup describe does not include a %s line: %w",
		bound.label,
		errFDBMissingBound,
	)
}
