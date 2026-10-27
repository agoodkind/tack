package ops

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"goodkind.io/tack/internal/config"
)

// retentionFDBExpireVersion preserves a native snapshot that can restore the
// retained boundary. The native expiration guard validates the exact version.
func retentionFDBExpireVersion(ctx context.Context, cfg *config.Config, name string, cutoff time.Time) (int64, error) {
	destination, err := fdbBlobstoreURL(cfg, name)
	if err != nil {
		return 0, err
	}
	retainedVersion, err := retentionFDBRetainedVersion(ctx, cfg, destination, cutoff)
	if err != nil {
		return 0, err
	}
	output, err := retentionFDBCommand(ctx, cfg, retentionFDBDescribe, "-d", destination, "--version-timestamps")
	if err != nil {
		return 0, err
	}
	var selectedBegin, selectedEnd int64
	for line := range strings.SplitSeq(output, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "Snapshot:") || !strings.Contains(line, "restorable=true") {
			continue
		}
		beginVersion, err := retentionFDBSnapshotVersion(line, "startVersion=")
		if err != nil {
			return 0, err
		}
		endVersion, err := retentionFDBSnapshotVersion(line, "endVersion=")
		if err != nil {
			return 0, err
		}
		if beginVersion <= 0 || endVersion < beginVersion {
			return 0, fmt.Errorf("FoundationDB backup %s snapshot version interval is invalid", name)
		}
		if endVersion <= retainedVersion && endVersion > selectedEnd {
			selectedBegin, selectedEnd = beginVersion, endVersion
		}
	}
	if selectedBegin == 0 {
		return 0, fmt.Errorf("FoundationDB backup %s does not establish a complete snapshot before retained cutoff %s", name, formatFDBTime(cutoff))
	}
	return selectedBegin, nil
}

func retentionFDBSnapshotVersion(line, key string) (int64, error) {
	_, suffix, found := strings.Cut(line, key)
	if !found {
		return 0, fmt.Errorf("FoundationDB snapshot does not report %s", key)
	}
	value, _, _ := strings.Cut(suffix, " ")
	version, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("FoundationDB snapshot %s is not a numeric version", key)
	}
	return version, nil
}
