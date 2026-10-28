package ops

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"goodkind.io/tack/internal/config"
)

// retentionFDBRetainedVersion uses the native timestamp conversion and verifies
// restorability without configuring a destination database or submitting a restore.
func retentionFDBRetainedVersion(ctx context.Context, cfg *config.Config, destination string, cutoff time.Time) (int64, error) {
	timestamp, err := fdbRestoreTimestampArg(cutoff.UTC())
	if err != nil {
		return 0, err
	}
	output, err := retentionFDBToolCommand(ctx, cfg, "fdbrestore", "start", []string{
		"--dryrun", "--timestamp", timestamp,
		"--orig-cluster-file", retentionFDBClusterFile, "-r", destination,
	})
	if err != nil {
		return 0, err
	}
	prefix := "Timestamp '" + timestamp + "' resolves to version "
	var resolved int64
	for line := range strings.SplitSeq(output, "\n") {
		value, found := strings.CutPrefix(strings.TrimSpace(line), prefix)
		if !found {
			continue
		}
		if resolved != 0 {
			return 0, errors.New("FoundationDB timestamp conversion reported multiple versions")
		}
		version, err := strconv.ParseInt(value, 10, 64)
		if err != nil || version <= 0 {
			return 0, errors.New("FoundationDB timestamp conversion reported an invalid version")
		}
		resolved = version
	}
	if resolved == 0 {
		return 0, errors.New("FoundationDB timestamp conversion did not report the retained version")
	}
	return resolved, nil
}
