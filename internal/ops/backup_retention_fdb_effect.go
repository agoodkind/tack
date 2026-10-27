package ops

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

const retentionFDBClusterFile = "/etc/foundationdb/fdb.cluster"

// retentionFDBOperation is one fdbbackup operation that retention runs.
type retentionFDBOperation string

const (
	retentionFDBStatus   retentionFDBOperation = "status"
	retentionFDBDescribe retentionFDBOperation = "describe"
	retentionFDBExpire   retentionFDBOperation = "expire"
	retentionFDBDelete   retentionFDBOperation = "delete"
)

// retentionFDBUsesSourceCluster maps each fdbbackup operation to whether it
// connects to the source cluster. fdbbackup delete reads only the backup
// container and does not accept a cluster file.
var retentionFDBUsesSourceCluster = map[retentionFDBOperation]bool{
	retentionFDBStatus: true, retentionFDBDescribe: true, retentionFDBExpire: true, retentionFDBDelete: false,
}

func revalidateRetentionFDB(ctx context.Context, cfg *config.Config, cutoff time.Time, action retentionFDBAction) error {
	actions, err := planRetentionFDB(ctx, cfg, cutoff)
	if err != nil {
		return err
	}
	selected := false
	for _, current := range actions {
		if current.Name == action.Name && current.Action == action.Action {
			selected = true
		}
	}
	if !selected {
		return fmt.Errorf("FoundationDB retention action %s for %s no longer satisfies selection guards", action.Action, action.Name)
	}
	return nil
}

func applyRetentionFDB(ctx context.Context, cfg *config.Config, cutoff time.Time, action retentionFDBAction) error {
	destination, err := fdbBlobstoreURL(cfg, action.Name)
	if err != nil {
		return err
	}
	arguments := []string{"-d", destination}
	switch action.Action {
	case retentionFDBExpire:
		timestamp, err := fdbRestoreTimestampArg(cutoff.UTC())
		if err != nil {
			return err
		}
		arguments = append(arguments, "--expire-before-version", strconv.FormatInt(action.ExpireBeforeVersion, 10), "--restorable-after-timestamp", timestamp)
	case retentionFDBDelete:
	case retentionFDBStatus, retentionFDBDescribe:
		return fmt.Errorf("FoundationDB operation %q does not remove backup data", action.Action)
	default:
		return fmt.Errorf("unsupported FoundationDB retention action %q", action.Action)
	}
	_, err = retentionFDBCommand(ctx, cfg, action.Action, arguments...)
	if err != nil {
		return &retentionDeleteError{cause: err, requested: 0, uncertain: true}
	}
	return nil
}

func retentionFDBCommand(ctx context.Context, cfg *config.Config, operation retentionFDBOperation, arguments ...string) (string, error) {
	command := append([]string{string(operation)}, arguments...)
	if retentionFDBUsesSourceCluster[operation] {
		command = append(command, "-C", retentionFDBClusterFile)
	}
	return retentionFDBToolCommand(ctx, cfg, "fdbbackup", command)
}

func retentionFDBToolCommand(ctx context.Context, cfg *config.Config, tool string, arguments []string) (string, error) {
	if !filepath.IsAbs(cfg.OpsFDBClusterDir) {
		return "", fmt.Errorf("FoundationDB retention requires an absolute TACK_OPS_FDB_CLUSTER_DIR")
	}
	privateArguments, privateFiles, err := retentionFDBPrivateArguments(cfg, arguments)
	if err != nil {
		return "", err
	}
	extraHosts, err := blobstoreExtraHosts(cfg.BackupS3Endpoint)
	if err != nil {
		return "", err
	}
	cli, err := newDockerClient(ctx)
	if err != nil {
		return "", err
	}
	defer cli.Close()
	result, err := runOneShot(ctx, cli, telemetry.L(ctx), runOneShotOptions{
		Image: cfg.BackupFDBImage, Network: cfg.BackupFDBNetwork,
		Entrypoint: []string{"/usr/bin/" + tool}, Cmd: privateArguments,
		Env: []string{
			"FDB_CLUSTER_FILE=" + retentionFDBClusterFile,
			"FDB_BLOB_CREDENTIALS=" + privateOneShotDirectory + "/" + retentionFDBAccountFileName,
		},
		Binds:      []string{cfg.OpsFDBClusterDir + ":/etc/foundationdb:ro"},
		ExtraHosts: extraHosts, Name: "",
		PrivateFiles: privateFiles, PrivateOutput: true,
	})
	if err != nil {
		if ctx.Err() != nil {
			wrapped := fmt.Errorf("FoundationDB retention %s: %w", arguments[0], ctx.Err())
			telemetry.L(ctx).ErrorContext(ctx, "backup.retention.fdb.command_failed", slog.String("err", wrapped.Error()))
			return "", wrapped
		}
		wrapped := fmt.Errorf("FoundationDB retention %s: %s", arguments[0], redactSecret(cfg, err.Error()))
		telemetry.L(ctx).ErrorContext(ctx, "backup.retention.fdb.command_failed", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	output := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
	if result.ExitCode != 0 {
		wrapped := fmt.Errorf("FoundationDB retention %s exited %d: %s", arguments[0], result.ExitCode, redactSecret(cfg, output))
		telemetry.L(ctx).ErrorContext(ctx, "backup.retention.fdb.command_failed", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	return output, nil
}
