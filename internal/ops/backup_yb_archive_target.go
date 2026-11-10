// The node archive command selects an export run and identifies the local
// guest within that run. Node timers run independently of the orchestrator
// timer. A timer invocation can succeed without requiring another upload.

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

// ybArchiveTarget records the selected manifest and this node's object-key
// prefix. complete reports whether every required node artifact exists
// under that prefix.
type ybArchiveTarget struct {
	manifest ybSnapshotManifest
	prefix   string
	complete bool
}

// resolveYBArchiveTarget selects this node's archive work. Discovery uses an
// empty runID and returns a nil target and nil error if a manifest does not
// exist or the newest manifest omits this node. A complete target requires
// the caller to skip the upload.
//
// The orchestrator uploads the manifest last. Discovery skips prefixes
// without a manifest, including unfinished runs. An explicit runID requires
// a manifest and a listed node; either omission returns an error.
func resolveYBArchiveTarget(
	ctx context.Context,
	cfg *config.Config,
	s3Client *s3.Client,
	nodeName string,
	runID string,
) (*ybArchiveTarget, error) {
	logger := telemetry.L(ctx)
	discovered := runID == ""
	var manifest ybSnapshotManifest
	if discovered {
		newest, found, err := newestUploadedYBSnapshotManifest(
			ctx,
			s3Client,
			cfg.BackupS3BucketMain,
		)
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
			logger.InfoContext(
				ctx,
				"backup.yb_archive.node_not_listed",
				slog.String("run_id", manifest.RunID),
				slog.String("node", nodeName),
			)

			return nil, nil
		}

		err := fmt.Errorf(
			"yb-archive-node: manifest for run %s does not list node %q",
			runID,
			nodeName,
		)
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", err.Error()))

		return nil, err
	}

	node := ybSnapshotManifestNode{Name: nodeName, Prefix: prefix}
	// Completion requires every node artifact, matching the restore drill and
	// snapshot cleanup. The earlier archive-only probe treated archives from
	// before inventories existed as complete. It never produced their missing
	// inventories, and the drill rejected those runs.
	//
	// An archive without its inventory now requires the entire node upload
	// again. The command creates a new tar archive and records its inventory
	// from that exact file. An inventory for newly captured bytes cannot verify
	// an earlier archive in the store.
	complete, err := ybNodeFullyArchived(
		manifest.RunID,
		node,
		func(key string) (bool, error) {
			return objectExists(ctx, s3Client, cfg.BackupS3BucketMain, key)
		},
	)
	if err != nil {
		return nil, err
	}

	if complete {
		logger.InfoContext(
			ctx,
			"backup.yb_archive.already_archived",
			slog.String("run_id", manifest.RunID),
			slog.String("node", nodeName),
			slog.String("key_prefix", ybNodeKeyPrefix(manifest.RunID, node)),
		)
	}

	return &ybArchiveTarget{
		manifest: manifest,
		prefix:   ybNodeKeyPrefix(manifest.RunID, node),
		complete: complete,
	}, nil
}

// localYBNodeName reads the hostname of the local YugabyteDB container. The
// Compose deployment sets it to the node's permanent name instead of an
// address. The masters report this identity in list_all_tablet_servers, and
// the manifest uses it to identify the node.
func localYBNodeName(ctx context.Context, cli *client.Client) (string, error) {
	logger := telemetry.L(ctx)
	insp, err := cli.ContainerInspect(
		ctx,
		yugabyteBackupContainer,
		client.ContainerInspectOptions{},
	)
	if err != nil {
		wrapped := fmt.Errorf(
			"inspect local yugabyte container %q: %w",
			yugabyteBackupContainer,
			err,
		)
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", wrapped.Error()))

		return "", wrapped
	}

	if insp.Container.Config == nil || strings.TrimSpace(insp.Container.Config.Hostname) == "" {
		wrapped := fmt.Errorf(
			"local yugabyte container %q has no hostname to identify this node",
			yugabyteBackupContainer,
		)
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", wrapped.Error()))

		return "", wrapped
	}

	return insp.Container.Config.Hostname, nil
}
