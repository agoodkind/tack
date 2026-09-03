// backup_restore_drill_fdb_pitr.go restores FoundationDB to an operator-chosen
// moment instead of the latest restorable point. It assembles the two engine
// commands the drill runs and reads one backup's restorable window. Which
// backup is restored, and the refusal of a target no backup can reach, live
// beside the backup selection; reading and rendering the target itself lives
// beside the flag that carries it.

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
	// fdbScratchClusterFile is the throwaway cluster's own cluster file inside
	// the drill container, written by the fdb overlay at boot. Every restore
	// the drill runs writes here and nowhere else.
	fdbScratchClusterFile = "/var/fdb/fdb.cluster"

	// fdbOrigClusterFilePath is where the live cluster's file is readable
	// inside the throwaway container. fdbrestore converts a wall-clock target
	// into a database version using the source cluster's version metadata, so
	// a point-in-time restore needs the source's cluster file even though it
	// writes only to the destination.
	//
	// The path is deliberately not /etc/foundationdb/fdb.cluster, which is
	// FoundationDB's last-resort default when no cluster file is named. The
	// image ships no such directory, so today a command inside the throwaway
	// that forgot its cluster-file flag fails loudly. Putting the live file on
	// the default path would turn that same mistake into a write against
	// production.
	fdbOrigClusterFilePath = "/tack-orig-fdb/fdb.cluster"

	// fdbOrigClusterMount binds the live cluster's client cluster file into
	// the throwaway container read-only. The host side is the file the fdb
	// service writes and the continuous backup already reads.
	fdbOrigClusterMount = "/etc/foundationdb/fdb.cluster:" + fdbOrigClusterFilePath + ":ro"

	// fdbDescribeTimeoutSeconds bounds the window lookup. The describe runs as
	// one synchronous exec that nothing watches, so without a bound a source
	// cluster or blobstore host that accepts the connection and then says
	// nothing would hang the drill forever. Bounding it is safe in the way
	// bounding the restore is not: describe reads the backup's metadata, whose
	// cost does not grow with the dataset, so no amount of stored data makes a
	// healthy describe approach this. The restore is bounded by inactivity
	// instead, because its work does scale.
	fdbDescribeTimeoutSeconds = 1800
)

// fdbRestoreCommand builds the argument vector the drill execs inside the
// throwaway container. A nil target restores the latest restorable point, which
// is all the drill could do before point-in-time restore existed. A target adds
// --timestamp and --orig-cluster-file, the pair fdbrestore needs to turn a
// wall-clock moment into a version. The destination stays the throwaway's own
// cluster file either way.
//
// The vector is never shell text. The launch hands it to sh as positional
// parameters and the engine gets it through "$@", so the destination URL is one
// argument however it is punctuated. That matters twice over: the URL carries
// the blobstore access key and secret, and the backup name inside it comes from
// whatever objects the bucket holds.
//
// Nothing here bounds how long the restore may run. A restore's duration scales
// with the dataset, so a total-work budget is a wall a growing corpus
// eventually hits; the drill watches the restore's progress counters instead
// and fails only one that stops moving.
//
// Flag names and the timestamp form are foundationdb 7.4.6's own, from
// `fdbrestore --help` in the pinned image.
func fdbRestoreCommand(destURL string, targetTime *time.Time) ([]string, error) {
	command := []string{
		"fdbrestore", "start",
		"--dest-cluster-file", fdbScratchClusterFile,
		"-r", destURL,
		"--waitfordone",
	}
	if targetTime == nil {
		return command, nil
	}
	timestamp, err := fdbRestoreTimestampArg(*targetTime)
	if err != nil {
		return nil, err
	}
	return append(command,
		"--timestamp", timestamp,
		"--orig-cluster-file", fdbOrigClusterFilePath,
	), nil
}

// fdbDescribeCommand builds the `fdbbackup describe` vector that reads the
// backup's restorable window. --version-timestamps turns the reported versions
// into wall-clock times and needs a cluster file to do it; that cluster file is
// the source cluster, because the versions in the backup are the source's. Like
// the restore, it is a vector so the credential-bearing URL is never shell text.
//
// Unlike the restore, it keeps a bound on its own run time, because nothing
// watches it and reading metadata is not work that grows with the dataset.
func fdbDescribeCommand(destURL string) []string {
	return []string{
		"timeout", strconv.Itoa(fdbDescribeTimeoutSeconds),
		"fdbbackup", "describe",
		"-d", destURL,
		"-C", fdbOrigClusterFilePath,
		"--version-timestamps",
	}
}

// describeFDBBackupWindow reads one backup's restorable window through the
// scratch container. Which backup the drill then restores from is decided
// beside the backup selection, against every session in the store.
//
// The describe output embeds the blobstore credentials in the destination URL,
// so it is redacted on the error path and never logged whole.
func describeFDBBackupWindow(
	ctx context.Context,
	r *restoreDrillCtx,
	containerName, backupName string,
) (fdbRestorableWindow, error) {
	destURL, err := fdbBlobstoreURL(r.Cfg, backupName)
	if err != nil {
		return fdbRestorableWindow{}, err
	}
	res, err := containerExec(ctx, r.Cli, containerName, fdbDescribeCommand(destURL))
	if err != nil {
		wrapped := fmt.Errorf("fdbbackup describe exec: %w", err)
		// One backup's describe failing is passed over by the selection, which
		// may still find an older backup that covers the target, so it is
		// warned here; the selection errors if no backup does.
		telemetry.L(ctx).WarnContext(ctx, "backup.restore_drill.fdb.describe_failed",
			slog.String("name", backupName), slog.String("err", wrapped.Error()))
		return fdbRestorableWindow{}, wrapped
	}
	if res.ExitCode != 0 {
		return fdbRestorableWindow{}, fmt.Errorf("fdbbackup describe exited %d: %s", res.ExitCode,
			redactSecret(r.Cfg, strings.TrimSpace(res.Stdout+" "+res.Stderr)))
	}
	return fdbRestorableWindowFromDescribe(res.Stdout)
}
