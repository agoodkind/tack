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

type retentionFDBOperation string

const (
	retentionFDBExpire retentionFDBOperation = "expire"
	retentionFDBDelete retentionFDBOperation = "delete"
)

func applyRetentionFDB(ctx context.Context, cfg *config.Config, cutoff time.Time, action retentionFDBAction) error {
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
	destination, err := fdbBlobstoreURL(cfg, action.Name)
	if err != nil {
		return err
	}
	arguments := []string{action.Action, "-d", destination}
	switch retentionFDBOperation(action.Action) {
	case retentionFDBExpire:
		timestamp, err := fdbRestoreTimestampArg(cutoff.UTC())
		if err != nil {
			return err
		}
		arguments = append(arguments, "--expire-before-version", strconv.FormatInt(action.ExpireBeforeVersion, 10), "--restorable-after-timestamp", timestamp)
	case retentionFDBDelete:
	default:
		return fmt.Errorf("unsupported FoundationDB retention action %q", action.Action)
	}
	_, err = retentionFDBCommand(ctx, cfg, arguments)
	if err != nil {
		return &retentionDeleteError{cause: err, requested: 0, uncertain: true}
	}
	return nil
}

func retentionFDBCommand(ctx context.Context, cfg *config.Config, arguments []string) (string, error) {
	if !filepath.IsAbs(cfg.OpsFDBClusterDir) {
		return "", fmt.Errorf("FoundationDB retention requires an absolute TACK_OPS_FDB_CLUSTER_DIR")
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
	command := append([]string{}, arguments...)
	if retentionFDBOperation(arguments[0]) != retentionFDBDelete {
		command = append(command, "-C", retentionFDBClusterFile)
	}
	result, err := runOneShot(ctx, cli, telemetry.L(ctx), runOneShotOptions{
		Image: cfg.BackupFDBImage, Network: cfg.BackupFDBNetwork,
		Entrypoint: []string{"/usr/bin/fdbbackup"}, Cmd: command,
		Env:        []string{"FDB_CLUSTER_FILE=" + retentionFDBClusterFile},
		Binds:      []string{cfg.OpsFDBClusterDir + ":/etc/foundationdb:ro"},
		ExtraHosts: extraHosts, Name: "",
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
