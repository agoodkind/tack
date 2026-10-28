package ops_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	// retentionFDBVersionLogKey is the version log (TACK-468) that retention
	// reads to convert its cutoff to a version.
	retentionFDBVersionLogKey = "backup-status/fdb-versions.json"
	// retentionFDBTimestampLayout is the timestamp layout of fdbbackup and
	// fdbrestore.
	retentionFDBTimestampLayout = "2006/01/02.15:04:05-0700"
	// retentionFDBLastLogLabel is the status line of the restorable point.
	retentionFDBLastLogLabel = "Last complete log version and timestamp"
)

// retentionFDBTestReading is one version log entry.
type retentionFDBTestReading struct {
	Version int64     `json:"version"`
	At      time.Time `json:"at"`
}

type retentionFDBTestVersionLog struct {
	Points []retentionFDBTestReading `json:"points"`
}

// retentionFDBTestRecordReading waits until fdbbackup status reports a
// restorable point stamped after after, and appends that point to the version
// log the way the staleness check does in production.
func retentionFDBTestRecordReading(t *testing.T, fixture *retentionCommandFixture, cluster string, after time.Time) retentionFDBTestReading {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		status := retentionFDBTestExec(t, fixture.config, cluster, []string{"fdbbackup", "status", "-C", "/var/fdb/fdb.cluster"})
		reading, found := retentionFDBTestStatusReading(status)
		if found && reading.At.After(after) {
			retentionFDBTestAppendReading(t, fixture, reading)
			return reading
		}
		select {
		case <-ctx.Done():
			t.Fatalf("fdbbackup status did not report a restorable point after %s", after)
		case <-ticker.C:
		}
	}
}

func retentionFDBTestStatusReading(status string) (retentionFDBTestReading, bool) {
	lowered := strings.ToLower(status)
	if !strings.Contains(lowered, " is restorable") || strings.Contains(lowered, " is not restorable") {
		return retentionFDBTestReading{}, false
	}
	for line := range strings.SplitSeq(status, "\n") {
		if !strings.Contains(line, retentionFDBLastLogLabel) {
			continue
		}
		_, value, _ := strings.Cut(line, " - ")
		rawVersion, rawTime, _ := strings.Cut(value, ",")
		version, versionErr := strconv.ParseInt(strings.TrimSpace(rawVersion), 10, 64)
		at, timeErr := time.Parse(retentionFDBTimestampLayout, strings.TrimSpace(rawTime))
		if versionErr != nil || timeErr != nil {
			return retentionFDBTestReading{}, false
		}
		return retentionFDBTestReading{Version: version, At: at.UTC()}, true
	}
	return retentionFDBTestReading{}, false
}

// retentionFDBTestRecordMoment converts at to a version with the native
// conversion of fdbrestore and appends that pair to the version log. The dry
// run prints the conversion first, and it may then refuse because backup name
// cannot restore at the requested time.
func retentionFDBTestRecordMoment(t *testing.T, fixture *retentionCommandFixture, cluster, name string, at time.Time) retentionFDBTestReading {
	t.Helper()
	timestamp := at.UTC().Format(retentionFDBTimestampLayout)
	output, _ := retentionFDBTestExecResult(t, fixture.config, cluster, []string{
		"fdbrestore", "start", "--dryrun", "--timestamp", timestamp,
		"--orig-cluster-file", "/var/fdb/fdb.cluster", "-r", retentionFDBTestURL(fixture.config, name),
	})
	prefix := "Timestamp '" + timestamp + "' resolves to version "
	for line := range strings.SplitSeq(output, "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), prefix); found {
			version, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				t.Fatalf("fdbrestore reported an invalid version for %s: %s", timestamp, value)
			}
			reading := retentionFDBTestReading{Version: version, At: at.UTC()}
			retentionFDBTestAppendReading(t, fixture, reading)
			return reading
		}
	}
	t.Fatalf("fdbrestore did not convert %s to a version: %s", timestamp, output)
	return retentionFDBTestReading{}
}

func retentionFDBTestAppendReading(t *testing.T, fixture *retentionCommandFixture, reading retentionFDBTestReading) {
	t.Helper()
	var versionLog retentionFDBTestVersionLog
	if body, found := fixture.store.object(retentionFDBVersionLogKey); found {
		if err := json.Unmarshal(body, &versionLog); err != nil {
			t.Fatalf("read the version log: %v", err)
		}
	}
	versionLog.Points = append(versionLog.Points, reading)
	body, err := json.Marshal(versionLog)
	if err != nil {
		t.Fatal(err)
	}
	fixture.store.put(retentionFDBVersionLogKey, body)
}
