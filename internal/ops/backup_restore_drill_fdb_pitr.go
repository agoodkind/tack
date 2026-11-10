// The drill builds FoundationDB commands to restore an operator's chosen
// time and read a backup's restorable window. Backup selection checks sessions
// newest first and rejects a target that every window excludes. Target parsing
// and rendering handle the flag input separately.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

const (
	// fdbScratchClusterFile specifies the scratch cluster's file inside the
	// drill container. The fdb overlay writes it at startup. Every drill restore
	// writes only to this cluster.
	fdbScratchClusterFile = "/var/fdb/fdb.cluster"

	// fdbOrigClusterFilePath specifies the live cluster file's location inside
	// the scratch container. fdbrestore needs the source cluster's version
	// metadata to convert a target time into a database version, even though
	// restore writes only to the destination.
	//
	// The path differs from /etc/foundationdb/fdb.cluster, FoundationDB's default
	// when a command omits the cluster-file argument. The original image
	// observation found that directory absent. A command that omits its flag then
	// fails instead of using the live cluster. Mounting the live file at the
	// default path would permit that mistake to write to production.
	fdbOrigClusterFilePath = "/tack-orig-fdb/fdb.cluster"

	// fdbOrigClusterMount mounts the live client cluster file read-only inside
	// the scratch container. The fdb service writes the host file, and continuous
	// backup already reads it.
	fdbOrigClusterMount = "/etc/foundationdb/fdb.cluster:" + fdbOrigClusterFilePath + ":ro"

	// fdbDescribeTimeoutSeconds limits the synchronous window lookup. The drill
	// does not monitor that exec. A source cluster or blobstore host that accepts
	// a connection without answering could otherwise block the drill indefinitely.
	//
	// The lookup reads metadata rather than restoring the dataset. The original
	// timeout rationale treats that work as independent of dataset size and
	// expects a healthy lookup to finish within the limit regardless of stored
	// data. Restore
	// work grows with the dataset and uses an inactivity limit instead.
	fdbDescribeTimeoutSeconds = 1800
)

// fdbRestoreCommand builds the argument vector for the scratch container.
// A nil target restores the latest restorable point, the drill's behavior
// before point-in-time restore. A target adds --timestamp and
// --orig-cluster-file to convert the chosen time into a version. Both forms
// write to the scratch cluster's own file.
//
// The shell receives the vector as positional parameters and executes "$@".
// It does not parse the URL as shell text. The URL includes blobstore
// credentials and a backup name derived from bucket objects, and it must
// remain one argument regardless of punctuation.
//
// The command does not limit total restore time. Dataset growth would
// eventually exceed a fixed budget. The drill monitors progress counters and
// applies an inactivity limit instead.
//
// The flag spellings and timestamp format follow the original
// fdbrestore --help observation from the pinned FoundationDB 7.4.6 image.
func fdbRestoreCommand(destURL string, targetTime *time.Time) ([]string, error) {
	command := []string{
		"fdbrestore",
		"start",
		"--dest-cluster-file",
		fdbScratchClusterFile,
		"-r",
		destURL,
		"--waitfordone",
	}
	if targetTime == nil {
		return command, nil
	}

	timestamp, err := fdbRestoreTimestampArg(*targetTime)
	if err != nil {
		return nil, err
	}

	return append(
		command,
		"--timestamp",
		timestamp,
		"--orig-cluster-file",
		fdbOrigClusterFilePath,
	), nil
}

// fdbDescribeCommand builds the fdbbackup describe vector for a backup's
// restorable window. --version-timestamps converts source versions to times
// and requires the source cluster file. The credential-bearing URL remains
// an argument instead of shell text.
//
// The command has a time limit because the drill does not monitor the exec.
// The original rationale treats metadata lookup work as independent of the
// dataset size, unlike restore work.
func fdbDescribeCommand(destURL string) []string {
	return []string{
		"timeout",
		strconv.Itoa(fdbDescribeTimeoutSeconds),
		"fdbbackup",
		"describe",
		"-d",
		destURL,
		"-C",
		fdbOrigClusterFilePath,
		"--version-timestamps",
	}
}

// describeFDBBackupWindow reads one session's restorable window through the
// scratch container. Backup selection checks session windows newest first
// and chooses the first session that covers the target.
//
// Describe output includes blobstore credentials in its URL. The error path
// redacts that output, and the drill does not log the complete output.
func describeFDBBackupWindow(
	ctx context.Context,
	r *restoreDrillCtx,
	containerName string,
	backupName string,
) (fdbRestorableWindow, error) {
	destURL, err := fdbBlobstoreURL(r.Cfg, backupName)
	if err != nil {
		return fdbRestorableWindow{}, err
	}

	res, err := containerExec(ctx, r.Cli, containerName, fdbDescribeCommand(destURL))
	if err != nil {
		wrapped := fmt.Errorf("fdbbackup describe exec: %w", err)
		// Selection skips a failed describe and can still choose an older backup
		// that covers the target. This path logs a warning; selection returns an
		// error when every backup excludes the target or fails its window lookup.
		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.restore_drill.fdb.describe_failed",
			slog.String("name", backupName),
			slog.String("err", wrapped.Error()),
		)

		return fdbRestorableWindow{}, wrapped
	}

	if res.ExitCode != 0 {
		return fdbRestorableWindow{}, fmt.Errorf(
			"fdbbackup describe exited %d: %s",
			res.ExitCode,
			redactSecret(r.Cfg, strings.TrimSpace(res.Stdout+" "+res.Stderr)),
		)
	}

	return fdbRestorableWindowFromDescribe(res.Stdout)
}
