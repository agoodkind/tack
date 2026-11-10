package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// ybAdminBinary identifies yb-admin inside the YugabyteDB image. The engine
// tools share a directory with yugabyted, which the Compose service replaces
// with a bind-mounted overlay.
const ybAdminBinary = "/home/yugabyte/bin/yb-admin"

// RunBackupYBPITRInit creates a YugabyteDB snapshot schedule for point-in-time
// recovery of the auth and audit database. Recovery can restore the SQL control
// plane to a point within the retention window.
//
// The command runs yb-admin create_snapshot_schedule in a one-shot container on
// the Tack Compose network. It does not invoke a host CLI. The ysql.<database>
// filter includes every schema, including public auth tables and the audit.*
// ledger.
//
// yb-admin requires this argument order:
//
//	yb-admin --master_addresses <masters> create_snapshot_schedule \
//	    <interval-minutes> <retention-minutes> ysql.<database>
//
// Both interval and retention use minutes. An existing schedule for the filter
// counts as success and logs backup.yb_pitr.exists. The command identifies that
// case from output text because yb-admin does not provide a distinct
// duplicate-schedule exit code.
func RunBackupYBPITRInit(ctx context.Context, cfg *config.Config) error {
	logger := telemetry.L(ctx)
	if err := requireBackupYBImage(ctx, cfg, "yb-pitr-init", "backup.yb_pitr.failed"); err != nil {
		return err
	}

	filter := "ysql." + cfg.YugabyteDB
	interval := strconv.Itoa(cfg.BackupYBPITRIntervalMinutes)
	retention := strconv.Itoa(cfg.BackupYBPITRRetentionMinutes)

	logger.InfoContext(
		ctx,
		"backup.yb_pitr.start",
		slog.String("masters", cfg.BackupYBMasterAddresses),
		slog.String("filter", filter),
		slog.Int("interval_minutes", cfg.BackupYBPITRIntervalMinutes),
		slog.Int("retention_minutes", cfg.BackupYBPITRRetentionMinutes),
	)

	cli, err := newDockerClient(ctx)
	if err != nil {
		return err
	}
	defer cli.Close()

	// The one-shot uses yb-admin as its entrypoint and Cmd for schedule arguments.
	// The Compose network provides Docker DNS for master hostnames. Shared access
	// settings include certificates when the cluster requires encryption. This call
	// reads exit status and output directly to recognize an existing schedule as
	// success.
	accessArgs, accessBinds := ybAdminClusterAccess(cfg)
	res, err := runOneShot(
		ctx,
		cli,
		logger,
		runOneShotOptions{
			Image:      cfg.BackupYBImage,
			Network:    cfg.BackupFDBNetwork,
			Entrypoint: []string{ybAdminBinary},
			Cmd:        append(accessArgs, "create_snapshot_schedule", interval, retention, filter),
			Env:        nil,
			Binds:      accessBinds,
			ExtraHosts: nil,
			Name:       "",
		},
	)
	if err != nil {
		wrapped := fmt.Errorf("run yb-admin create_snapshot_schedule for %q: %w", filter, err)
		logger.ErrorContext(ctx, "backup.yb_pitr.failed", slog.String("err", wrapped.Error()))

		return wrapped
	}

	combined := res.Stdout + "\n" + res.Stderr
	if res.ExitCode != 0 {
		if isScheduleAlreadyExists(combined) {
			logger.InfoContext(ctx, "backup.yb_pitr.exists", slog.String("filter", filter))
			return nil
		}

		err := fmt.Errorf(
			"yb-admin create_snapshot_schedule exited %d: %s",
			res.ExitCode,
			strings.TrimSpace(combined),
		)
		logger.ErrorContext(ctx, "backup.yb_pitr.failed", slog.String("err", err.Error()))

		return err
	}

	// Some yb-admin builds report an existing schedule with exit status 0. That
	// result also counts as success.
	if isScheduleAlreadyExists(combined) {
		logger.InfoContext(ctx, "backup.yb_pitr.exists", slog.String("filter", filter))
		return nil
	}

	scheduleID := parseScheduleID(res.Stdout)
	if scheduleID != "" {
		logger.InfoContext(
			ctx,
			"backup.yb_pitr.created",
			slog.String("filter", filter),
			slog.String("schedule_id", scheduleID),
		)

		return nil
	}

	logger.InfoContext(ctx, "backup.yb_pitr.completed", slog.String("filter", filter))

	return nil
}

// parseScheduleID extracts the schedule_id UUID from yb-admin JSON such as
// {"schedule_id": "<uuid>"}. It returns an empty string when output is empty,
// invalid JSON, or omits the field. The caller logs completion without a parsed
// ID instead of returning an error.
func parseScheduleID(stdout string) string {
	var parsed struct {
		ScheduleID string `json:"schedule_id"`
	}
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return ""
	}

	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return ""
	}

	return parsed.ScheduleID
}

// isScheduleAlreadyExists checks yb-admin output for an existing snapshot
// schedule. The duplicate case does not have a dedicated exit code; the
// function examines message text for a schedule and a duplicate indication.
func isScheduleAlreadyExists(output string) bool {
	lower := strings.ToLower(output)
	if !strings.Contains(lower, "schedule") {
		return false
	}

	return strings.Contains(lower, "already exists") ||
		strings.Contains(lower, "already scheduled") ||
		strings.Contains(lower, "duplicate")
}
