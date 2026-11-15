package ops

import (
	"fmt"
	"strings"
	"time"
)

type fdbTimestampBound struct {
	label string
	value time.Time
}

// UnmarshalText decodes the first matching FoundationDB timestamp bound.
//
// It reads the timestamp between the first opening and last closing parenthesis.
// Invalid or missing bounds return an error without emitting logs.
//
// For example:
//
//	bound := fdbTimestampBound{label: fdbDescribeMinLabel}
//	err := bound.UnmarshalText([]byte("MinRestorableVersion: 123 (2026/08/30.01:07:23+0000)"))
func (bound *fdbTimestampBound) UnmarshalText(text []byte) error {
	for line := range strings.SplitSeq(string(text), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, bound.label) {
			continue
		}

		openIndex := strings.Index(trimmed, "(")
		closeIndex := strings.LastIndex(trimmed, ")")
		if openIndex < 0 || closeIndex < openIndex {
			return fmt.Errorf(
				"%w: %q; describe needs --version-timestamps and a source cluster file",
				errFDBTimestamp,
				trimmed,
			)
		}

		stamp := trimmed[openIndex+1 : closeIndex]
		at, err := time.Parse(fdbStatusTimestampLayout, stamp)
		if err != nil {
			return fmt.Errorf(
				"fdbbackup describe line %q does not contain a valid timestamp;"+
					" describe needs --version-timestamps and a source cluster file: %w",
				trimmed,
				err,
			)
		}

		bound.value = at

		return nil
	}

	return fmt.Errorf(
		"fdbbackup describe does not include a %s line: %w",
		bound.label,
		errFDBMissingBound,
	)
}
