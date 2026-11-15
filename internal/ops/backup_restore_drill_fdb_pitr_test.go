package ops

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const drillDestURL = "blobstore://drill-access:drill-secret@fdb-blobstore-host:8333/20260830T000000Z" + // gitleaks:allow test placeholder
	"?bucket=tack-backups&region=us-east-1&secure_connection=0"

// hostileDestURL includes shell syntax in its backup name. Object storage supplies
// backup names, which commands must treat as data. The URL includes test credentials.
const hostileDestURL = "blobstore://drill-access:drill-secret@fdb-blobstore-host:8333/" + // gitleaks:allow test placeholder
	"20260830T000000Z'; printf pwned > /tmp/pwned; '?bucket=tack-backups"

// TestFDBRestoreCommandRestoresLatestByDefault checks the command for an absent
// target. It must omit both the timestamp and source cluster arguments.
func TestFDBRestoreCommandRestoresLatestByDefault(t *testing.T) {
	got, err := fdbRestoreCommand(drillDestURL, nil)
	if err != nil {
		t.Fatalf("fdbRestoreCommand without a target: %v", err)
	}

	want := []string{
		"fdbrestore",
		"start",
		"--dest-cluster-file",
		"/var/fdb/fdb.cluster",
		"-r",
		drillDestURL,
		"--waitfordone",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("default restore command changed:\n got %q\nwant %q", got, want)
	}
}

// TestFDBRestoreCommandIncludesTargetAndSourceCluster checks the timestamp,
// source-cluster, and destination-cluster arguments without running a restore.
func TestFDBRestoreCommandIncludesTargetAndSourceCluster(t *testing.T) {
	target := time.Date(2026, 8, 30, 1, 5, 0, 0, time.UTC)

	got, err := fdbRestoreCommand(drillDestURL, &target)
	if err != nil {
		t.Fatalf("fdbRestoreCommand with a whole-second target: %v", err)
	}

	want := []string{
		"fdbrestore",
		"start",
		"--dest-cluster-file",
		"/var/fdb/fdb.cluster",
		"-r",
		drillDestURL,
		"--waitfordone",
		"--timestamp",
		"2026/08/30.01:05:00+0000",
		"--orig-cluster-file",
		"/tack-orig-fdb/fdb.cluster",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("targeted restore command changed:\n got %q\nwant %q", got, want)
	}

	// Commands without explicit cluster-file flags use FoundationDB's default
	// path. Mount the live source file outside that path in the temporary container.
	if strings.Contains(fdbOrigClusterFilePath, "/etc/foundationdb/") {
		t.Fatalf("source cluster file must not use FoundationDB's default path: %q", fdbOrigClusterFilePath)
	}
}

// TestFDBCommandsIncludeDestinationAsSingleArgument requires the complete URL
// exactly once in each command. Neither command may embed a shell program.
// Restore launch uses shell positional parameters rather than URL text as code.
func TestFDBCommandsIncludeDestinationAsSingleArgument(t *testing.T) {
	target := time.Date(2026, 8, 30, 1, 5, 0, 0, time.UTC)
	restore, err := fdbRestoreCommand(hostileDestURL, &target)
	if err != nil {
		t.Fatalf("fdbRestoreCommand: %v", err)
	}

	describe := fdbDescribeCommand(hostileDestURL)
	for name, command := range map[string][]string{
		"restore command includes destination as one argument":  restore,
		"describe command includes destination as one argument": describe,
	} {
		t.Run(name, func(t *testing.T) {
			if slices.Contains(command, "sh") || slices.Contains(command, "-c") {
				t.Fatalf("engine command must not embed a shell program: %q", command)
			}

			destinationArguments := 0
			for _, argument := range command {
				if argument == hostileDestURL {
					destinationArguments++
					continue
				}
				if strings.Contains(argument, "printf pwned > /tmp/pwned") {
					t.Fatalf("shell syntax appears outside the complete destination argument: %q", command)
				}
			}
			if destinationArguments != 1 {
				t.Fatalf("destination URL appears in %d complete arguments, want 1: %q", destinationArguments, command)
			}
		})
	}
}

// TestFDBTargetTimeMountUsesReadOnlySourceCluster checks the source file's
// read-only mount at the path that the restore command specifies.
func TestFDBTargetTimeMountUsesReadOnlySourceCluster(t *testing.T) {
	if !strings.HasSuffix(fdbOrigClusterMount, ":"+fdbOrigClusterFilePath+":ro") {
		t.Fatalf("source cluster mount must be read-only at the specified path: %q", fdbOrigClusterMount)
	}
}

// TestFDBDescribeCommandReadsWindowFromSourceCluster checks the source cluster,
// timestamp flag, and timeout arguments for the metadata query.
func TestFDBDescribeCommandReadsWindowFromSourceCluster(t *testing.T) {
	got := fdbDescribeCommand(drillDestURL)
	want := []string{
		"timeout",
		strconv.Itoa(fdbDescribeTimeoutSeconds),
		"fdbbackup",
		"describe",
		"-d",
		drillDestURL,
		"-C",
		fdbOrigClusterFilePath,
		"--version-timestamps",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("describe command changed:\n got %q\nwant %q", got, want)
	}
}
