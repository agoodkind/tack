package ops

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

var ybUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ybDrillRestoreWaitTimeout bounds the wait for restore_snapshot to reach
// RESTORED on the scratch node. Warm, 130 tablets restore in ~20s; the budget
// covers a cold page cache right after a deploy, where the same restoration
// exceeded 90s (observed 2026-08-29 on the QA owner).
const ybDrillRestoreWaitTimeout = 5 * time.Minute

// importAndRestoreYBSnapshot imports the exported snapshot metadata, copies the
// exported tablet files into the new tablets the import created, and runs
// restore_snapshot. The schema must already be applied so the tables exist.
// inventories are the staged records of what each node's archive carries, in
// manifest order; each node's archive is extracted into its own directory so
// replicas of the same tablet from different nodes never mix files, and each
// extraction is checked against its own node's record before any of it is
// copied.
func importAndRestoreYBSnapshot(
	ctx context.Context,
	r *restoreDrillCtx,
	container, database, exportSnap string,
	inventories []ybArchiveInventory,
) error {
	logger := telemetry.L(ctx)
	master := container + ":7100"

	importRes, err := containerExec(ctx, r.Cli, container,
		[]string{
			ybAdminBinary, "--master_addresses", master, "import_snapshot",
			ybDrillArtifactPath(ybSnapshotMetadataObject), database,
		})
	if err != nil {
		return fmt.Errorf("import_snapshot exec: %w", err)
	}
	if importRes.ExitCode != 0 {
		wrapped := fmt.Errorf("import_snapshot exited %d: %s", importRes.ExitCode, strings.TrimSpace(importRes.Stderr))
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	remaps, err := parseYBSnapshotMapping(importRes.Stdout)
	if err != nil {
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", err.Error()))
		return err
	}
	if len(remaps) == 0 {
		wrapped := fmt.Errorf("import_snapshot produced no tablet mapping")
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	newSnap, err := newestYBSnapshotID(ctx, r, container, master)
	if err != nil {
		return err
	}

	layout := drillPlacementLayout(r.Cfg.BackupYBRocksDBDir)
	extractScript := ybTabletExtractScript(layout.ExportRoot, ybDrillArtifactMount)
	extractions := make([]ybNodeExtraction, 0, len(inventories))
	for _, inventory := range inventories {
		if extractRes, err := containerExec(ctx, r.Cli, container,
			[]string{"sh", "-c", extractScript, "sh", inventory.Node}); err != nil || extractRes.ExitCode != 0 {
			wrapped := fmt.Errorf("extract tablets for node %s: exit %d: %w", inventory.Node, extractRes.ExitCode, err)
			logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
			return wrapped
		}
		// A clean extraction is tar's own account of itself. What the export
		// recorded is the independent one, and it is the one each tablet is
		// judged against below.
		missing, err := checkYBNodeExtraction(ctx, r, container, layout.ExportRoot, inventory)
		if err != nil {
			return err
		}
		extractions = append(extractions, newYBNodeExtraction(inventory, missing))
	}

	placements, defects := chooseYBTabletReplicas(remaps, layout.ExportRoot, exportSnap, extractions)
	if len(defects) > 0 {
		wrapped := ybTabletDefectError(defects, countDistinctTablets(remaps))
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	for _, script := range ybPlacementScripts(placements, layout, newSnap) {
		if placeRes, err := containerExec(ctx, r.Cli, container, []string{"sh", "-c", script}); err != nil || placeRes.ExitCode != 0 {
			wrapped := fmt.Errorf("place tablet files: exit %d: %s: %w", placeRes.ExitCode, strings.TrimSpace(placeRes.Stderr), err)
			logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
			return wrapped
		}
	}
	// Every tablet the import created has to have been found in the export
	// before the restore is allowed to proceed. Without this the copies that
	// found nothing are indistinguishable from copies that worked, and the
	// row assertion downstream passes on whatever fraction did land.
	if err := auditYBPlacement(ctx, r, container, layout, remaps); err != nil {
		return err
	}
	logger.InfoContext(ctx, "backup.restore_drill.yb.snapshot_imported",
		slog.String("new_snapshot_id", newSnap))

	if restoreRes, err := containerExec(ctx, r.Cli, container,
		[]string{ybAdminBinary, "--master_addresses", master, "restore_snapshot", newSnap}); err != nil || restoreRes.ExitCode != 0 {
		wrapped := fmt.Errorf("restore_snapshot exit %d: %s: %w", restoreRes.ExitCode, strings.TrimSpace(restoreRes.Stderr), err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	// Wait for the restoration to finish, and fail on timeout: reading the
	// tables while the restoration is still applying returns the pre-restore
	// empty rows, which the row assertion would misreport as data loss (the
	// 2026-08-29 drill failure: a cold-cached guest needed more than 90s for
	// 130 tablets that restore in ~20s warm).
	if err := waitExecOK(ctx, r, container, ybDrillRestoreWaitTimeout, nil,
		[]string{"sh", "-c", ybAdminBinary + " --master_addresses " + master + " list_snapshot_restorations | grep -q RESTORED"}); err != nil {
		wrapped := fmt.Errorf("snapshot restoration did not reach RESTORED: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}

// newestYBSnapshotID returns the single snapshot id on the scratch cluster (the
// imported one); a fresh yugabyted has no schedule, so it is unambiguous.
func newestYBSnapshotID(ctx context.Context, r *restoreDrillCtx, container, master string) (string, error) {
	logger := telemetry.L(ctx)
	res, err := containerExec(ctx, r.Cli, container,
		[]string{ybAdminBinary, "--master_addresses", master, "list_snapshots"})
	if err != nil || res.ExitCode != 0 {
		wrapped := fmt.Errorf("list_snapshots exit %d: %w", res.ExitCode, err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	for line := range strings.SplitSeq(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && ybUUIDPattern.MatchString(fields[0]) {
			return fields[0], nil
		}
	}
	wrapped := fmt.Errorf("no snapshot id found after import_snapshot")
	logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
	return "", wrapped
}
