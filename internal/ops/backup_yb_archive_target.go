// backup_yb_archive_target.go decides which export run, if any, the guest
// running the archive command owes work to, and what this guest's identity in
// that run is. The node timers fire independently of the orchestrator's, so
// finding nothing to do is a normal outcome here rather than a failure.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// ybArchiveTarget is the resolved work for one archive invocation: the run's
// manifest and the object key prefix this node must fill.
type ybArchiveTarget struct {
	manifest ybSnapshotManifest
	prefix   string
}

// resolveYBArchiveTarget decides what, if anything, this node must archive.
// A nil target with a nil error is the quiet nothing-to-do outcome, reachable
// only in discovery mode (empty runID): no run has a manifest yet, the newest
// manifest does not list this node, or every artifact this node owes the run
// is already uploaded. The orchestrator uploads the manifest last, so
// discovery walks past manifest-less run prefixes (runs that never finished)
// instead of treating them as errors. With an explicit runID a missing
// manifest or an unlisted node is an error, because the operator asked for
// that run specifically.
func resolveYBArchiveTarget(
	ctx context.Context,
	cfg *config.Config,
	s3Client *s3.Client,
	nodeName, runID string,
) (*ybArchiveTarget, error) {
	logger := telemetry.L(ctx)
	discovered := runID == ""
	var manifest ybSnapshotManifest
	if discovered {
		newest, found, err := newestUploadedYBSnapshotManifest(ctx, s3Client, cfg.BackupS3BucketMain)
		if err != nil {
			return nil, err
		}
		if !found {
			logger.InfoContext(ctx, "backup.yb_archive.no_manifest", slog.String("node", nodeName))
			return nil, nil
		}
		manifest = newest
	} else {
		fetched, err := fetchYBSnapshotManifest(ctx, s3Client, cfg.BackupS3BucketMain, runID)
		if err != nil {
			return nil, err
		}
		manifest = fetched
	}

	prefix, listed := manifest.nodePrefix(nodeName)
	if !listed {
		if discovered {
			logger.InfoContext(ctx, "backup.yb_archive.node_not_listed",
				slog.String("run_id", manifest.RunID), slog.String("node", nodeName))
			return nil, nil
		}
		err := fmt.Errorf("yb-archive-node: manifest for run %s does not list node %q", runID, nodeName)
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", err.Error()))
		return nil, err
	}

	node := ybSnapshotManifestNode{Name: nodeName, Prefix: prefix}
	// Done means every artifact this node owes the run is in the store, by the
	// same rule the restore drill and the snapshot cleanup apply. Probing the
	// archive alone read an archive uploaded before inventories existed as
	// finished, so no inventory was ever written for it: the drill refused the
	// run for lacking one and nothing healed it. A node with such an archive
	// redoes its whole run, re-tarring the snapshot and uploading the archive
	// again beside the inventory, because the inventory is recorded from the
	// archive it is uploaded with and cannot vouch for bytes another run put in
	// the store.
	complete, err := ybNodeFullyArchived(manifest.RunID, node, func(key string) (bool, error) {
		return objectExists(ctx, s3Client, cfg.BackupS3BucketMain, key)
	})
	if err != nil {
		return nil, err
	}
	if complete {
		logger.InfoContext(ctx, "backup.yb_archive.already_archived",
			slog.String("run_id", manifest.RunID), slog.String("node", nodeName),
			slog.String("key_prefix", ybNodeKeyPrefix(manifest.RunID, node)))
		return nil, nil
	}
	return &ybArchiveTarget{manifest: manifest, prefix: ybNodeKeyPrefix(manifest.RunID, node)}, nil
}

// localYBNodeName is the node name this guest's yugabyte container announces
// to the cluster. The compose deploy sets the container's hostname to the
// node's permanent name (never an address), the same name the masters report
// in list_all_tablet_servers and the manifest lists, so the container hostname
// is the node's identity.
func localYBNodeName(ctx context.Context, cli *client.Client) (string, error) {
	logger := telemetry.L(ctx)
	insp, err := cli.ContainerInspect(ctx, yugabyteBackupContainer, client.ContainerInspectOptions{})
	if err != nil {
		wrapped := fmt.Errorf("inspect local yugabyte container %q: %w", yugabyteBackupContainer, err)
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	if insp.Container.Config == nil || strings.TrimSpace(insp.Container.Config.Hostname) == "" {
		wrapped := fmt.Errorf("local yugabyte container %q has no hostname to identify this node", yugabyteBackupContainer)
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	return insp.Container.Config.Hostname, nil
}
