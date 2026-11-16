// Archive timers select work independently of the export orchestrator.
// Discovery can legitimately find an unpublished run or an unlisted node
// without requiring an archive upload.

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

// ybArchiveTarget records the selected manifest, node prefix, and whether all
// required node artifacts already exist.
type ybArchiveTarget struct {
	manifest ybSnapshotManifest
	prefix   string
	complete bool
}

// resolveYBArchiveTarget returns a nil target without error only during
// discovery when manifests are absent or the newest manifest omits this node.
// Discovery skips prefixes without manifests because upload publishes them
// last.
// Explicit run IDs instead require a manifest and node membership.
// complete=true suppresses uploads only after every required node artifact
// exists.
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
	// Archive presence alone cannot establish completeness. A missing
	// inventory requires recreating and uploading both objects; a new
	// inventory cannot verify a different archive's historical bytes.
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

// localYBNodeName reads the permanent container hostname that masters report
// in list_all_tablet_servers and manifests use as node identity.
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
		wrapped := localYBHostnameMissingError{Container: yugabyteBackupContainer}
		logger.ErrorContext(ctx, "backup.yb_archive.failed", slog.String("err", wrapped.Error()))

		return "", wrapped
	}

	return insp.Container.Config.Hostname, nil
}

type localYBHostnameMissingError struct {
	Container string
}

func (failure localYBHostnameMissingError) Error() string {
	return fmt.Sprintf(
		"local yugabyte container %q does not specify a hostname to identify this node",
		failure.Container,
	)
}
