// FoundationDB point-in-time restore commands validate the selected backup's
// window and restore to the operator's requested moment.

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
	// fdbScratchClusterFile specifies the scratch cluster file that the overlay
	// writes at startup. Every drill restore writes only to this cluster.
	fdbScratchClusterFile = "/var/fdb/fdb.cluster"

	// fdbOrigClusterFilePath specifies the read-only source cluster file used
	// to convert timestamps to source database versions. It deliberately differs
	// from FoundationDB's default /etc/foundationdb/fdb.cluster path. The image
	// does not provide that default directory. A missing cluster-file flag must
	// fail rather than select the live cluster and permit a production write.
	fdbOrigClusterFilePath = "/tack-orig-fdb/fdb.cluster"

	// fdbOrigClusterMount binds the live client cluster file read-only, using
	// the host file that the FDB service writes and continuous backup reads.
	fdbOrigClusterMount = "/etc/foundationdb/fdb.cluster:" + fdbOrigClusterFilePath + ":ro"

	// fdbDescribeTimeoutSeconds limits the synchronous, unmonitored metadata
	// lookup. A source that accepts a connection but never answers must not
	// block the drill indefinitely. Restore execution instead uses an
	// inactivity limit because its work grows with the dataset.
	fdbDescribeTimeoutSeconds = 1800
)

// fdbRestoreCommand builds engine arguments for the scratch destination.
// A nil target restores the latest point. An explicit target adds --timestamp
// and --orig-cluster-file for conversion to a source database version.
//
// The launch shell expands positional arguments with "$@" without parsing
// their contents as shell code. The URL includes credentials and an untrusted
// backup name from the bucket. Every destination remains the scratch cluster.
// Monitoring permits unlimited total work while detecting inactivity.
// Flag and timestamp formats follow `fdbrestore --help` in FoundationDB 7.4.6.
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

// fdbDescribeCommand reads the restorable window with --version-timestamps
// using the source cluster to convert its backup versions to times. Arguments
// prevent shell parsing of the credential-bearing URL. A timeout bounds this
// unmonitored metadata lookup rather than the restore itself.
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

// describeFDBBackupWindow reads one session through the scratch container.
// Describe output includes credentials in the destination URL. Error output
// requires redaction, and logs must not include the complete output.
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
		// Selection can still use an older covering backup. This warning
		// records the failed session; selection fails only after every candidate.
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
