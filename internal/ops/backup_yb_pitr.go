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

// ybAdminBinary is the yb-admin path inside the yugabyte image. The image
// ships its CLIs under /home/yugabyte/bin alongside yugabyted, which the
// compose service bind-mounts the overlay over.
const ybAdminBinary = "/home/yugabyte/bin/yb-admin"

// RunBackupYBPITRInit creates a YugabyteDB point-in-time-recovery snapshot
// schedule for the auth + audit database so a fresh deploy can roll the SQL
// control plane back to any point within the retention window. It runs
// yb-admin create_snapshot_schedule inside a one-shot container on the tack
// compose network, never shelling out to a CLI on the host.
//
// The yb-admin argument order is fixed by the tool:
//
//	yb-admin --master_addresses <masters> create_snapshot_schedule \
//	    <interval-minutes> <retention-minutes> ysql.<database>
//
// Both the interval and the retention are in minutes (yb-admin's documented
// units for create_snapshot_schedule). The ysql.<database> filter covers every
// schema in that database, so a single schedule protects both the public auth
// tables and the audit.* ledger in the tack database.
//
// Idempotency: a schedule that already exists for the filter is treated as
// success and logged backup.yb_pitr.exists. yb-admin's duplicate-schedule
// behavior is detected from its output text because the tool does not expose a
// distinct exit code for that case.
func RunBackupYBPITRInit(ctx context.Context, cfg *config.Config) error {
	logger := telemetry.L(ctx)
	filter := "ysql." + cfg.YugabyteDB
	interval := strconv.Itoa(cfg.BackupYBPITRIntervalMinutes)
	retention := strconv.Itoa(cfg.BackupYBPITRRetentionMinutes)

	logger.InfoContext(ctx, "backup.yb_pitr.start",
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

	// yb-admin is the entrypoint; the schedule arguments follow in Cmd. The
	// one-shot joins the compose network so the embedded Docker DNS resolves
	// the masters hostname.
	res, err := runOneShot(ctx, cli, logger, runOneShotOptions{
		Image:      cfg.BackupYBPITRImage,
		Network:    cfg.BackupFDBNetwork,
		Entrypoint: []string{ybAdminBinary},
		Cmd: []string{
			"--master_addresses", cfg.BackupYBMasterAddresses,
			"create_snapshot_schedule",
			interval,
			retention,
			filter,
		},
		Env:        nil,
		Binds:      nil,
		ExtraHosts: nil,
		Name:       "",
	})
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
		err := fmt.Errorf("yb-admin create_snapshot_schedule exited %d: %s",
			res.ExitCode, strings.TrimSpace(combined))
		logger.ErrorContext(ctx, "backup.yb_pitr.failed", slog.String("err", err.Error()))
		return err
	}

	// yb-admin can exit 0 while reporting a pre-existing schedule on some
	// builds; treat that as the idempotent success path too.
	if isScheduleAlreadyExists(combined) {
		logger.InfoContext(ctx, "backup.yb_pitr.exists", slog.String("filter", filter))
		return nil
	}

	scheduleID := parseScheduleID(res.Stdout)
	if scheduleID != "" {
		logger.InfoContext(ctx, "backup.yb_pitr.created",
			slog.String("filter", filter),
			slog.String("schedule_id", scheduleID),
		)
		return nil
	}

	logger.InfoContext(ctx, "backup.yb_pitr.completed", slog.String("filter", filter))
	return nil
}

// parseScheduleID extracts the schedule_id UUID from yb-admin's JSON output.
// On success yb-admin prints `{"schedule_id": "<uuid>"}`. An empty return means
// the field was absent, which the caller treats as a completed-but-unparsed
// success rather than a failure.
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

// isScheduleAlreadyExists reports whether yb-admin's output indicates a
// snapshot schedule already exists for the requested filter. yb-admin does not
// expose a dedicated exit code for the duplicate case, so the duplicate is
// detected from the message text, which mentions an already-existing schedule.
func isScheduleAlreadyExists(output string) bool {
	lower := strings.ToLower(output)
	if !strings.Contains(lower, "schedule") {
		return false
	}
	return strings.Contains(lower, "already exists") ||
		strings.Contains(lower, "already scheduled") ||
		strings.Contains(lower, "duplicate")
}
