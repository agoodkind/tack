package ops

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

var ybUUIDPattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
)

// importAndRestoreYBSnapshot imports exported snapshot metadata, copies
// exported tablet files into imported tablets, and runs restore_snapshot.
// Apply the schema first to create the tables. inventories supplies staged
// node archive records in manifest order. Each node archive uses a separate
// extraction directory to prevent replica files from mixing. The drill checks
// each extraction against its node's inventory before copying files.
func importAndRestoreYBSnapshot(
	ctx context.Context,
	r *restoreDrillCtx,
	container string,
	database string,
	exportSnap string,
	inventories []ybArchiveInventory,
) error {
	logger := telemetry.L(ctx)
	master := ybScratchHost(container) + ":7100"

	remaps, newSnap, err := importYBSnapshotMetadata(ctx, r, container, database, master, logger)
	if err != nil {
		return err
	}

	layout := drillPlacementLayout(r.Cfg.BackupYBRocksDBDir)
	placements, err := prepareYBTabletPlacement(
		ctx,
		r,
		container,
		layout,
		exportSnap,
		inventories,
		remaps,
		logger,
	)
	if err != nil {
		return err
	}

	for _, script := range ybPlacementScripts(placements, layout, newSnap) {
		if placeRes, err := containerExec(
			ctx,
			r.Cli,
			container,
			[]string{"sh", "-c", script},
		); err != nil ||
			placeRes.ExitCode != 0 {
			wrapped := fmt.Errorf(
				"place tablet files: exit %d: %s: %w",
				placeRes.ExitCode,
				strings.TrimSpace(placeRes.Stderr),
				err,
			)
			logger.ErrorContext(
				ctx,
				"backup.restore_drill.yb.failed",
				slog.String("err", wrapped.Error()),
			)

			return wrapped
		}
	}

	// Require a successful placement record for every imported tablet before
	// restore. Without this check, copies that did not find files could appear
	// successful, and the later row assertion could pass with only some files
	// restored.
	if err := auditYBPlacement(ctx, r, container, layout, remaps); err != nil {
		return err
	}

	logger.InfoContext(
		ctx,
		"backup.restore_drill.yb.snapshot_imported",
		slog.String("new_snapshot_id", newSnap),
	)

	return restoreImportedYBSnapshot(ctx, r, container, master, newSnap, logger)
}

// newestYBSnapshotID returns the imported snapshot ID from the scratch cluster.
// A fresh yugabyted cluster does not have a schedule and contains only that
// snapshot.
func newestYBSnapshotID(
	ctx context.Context,
	r *restoreDrillCtx,
	container string,
	master string,
) (string, error) {
	logger := telemetry.L(ctx)
	res, err := containerExec(
		ctx,
		r.Cli,
		container,
		[]string{ybAdminBinary, "--master_addresses", master, "list_snapshots"},
	)
	if err != nil || res.ExitCode != 0 {
		wrapped := fmt.Errorf("list_snapshots exit %d: %w", res.ExitCode, err)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.yb.failed",
			slog.String("err", wrapped.Error()),
		)

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

func importYBSnapshotMetadata(
	ctx context.Context,
	drill *restoreDrillCtx,
	container string,
	database string,
	master string,
	logger *slog.Logger,
) ([]ybTabletRemap, string, error) {
	importRes, err := containerExec(
		ctx,
		drill.Cli,
		container,
		[]string{
			ybAdminBinary,
			"--master_addresses",
			master,
			"import_snapshot",
			ybDrillArtifactPath(ybSnapshotMetadataObject),
			database,
		},
	)
	if err != nil {
		return nil, "", fmt.Errorf("import_snapshot exec: %w", err)
	}

	if importRes.ExitCode != 0 {
		wrapped := &ybSnapshotImportExitError{
			exitCode: importRes.ExitCode,
			stderr:   strings.TrimSpace(importRes.Stderr),
		}
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.yb.failed",
			slog.String("err", wrapped.Error()),
		)

		return nil, "", wrapped
	}

	remaps, err := parseYBSnapshotMapping(importRes.Stdout)
	if err != nil {
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", err.Error()))

		return nil, "", err
	}

	if len(remaps) == 0 {
		wrapped := errYBTabletMappingMissing
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.yb.failed",
			slog.String("err", wrapped.Error()),
		)

		return nil, "", wrapped
	}

	newSnap, err := newestYBSnapshotID(ctx, drill, container, master)
	if err != nil {
		return nil, "", err
	}

	return remaps, newSnap, nil
}

func prepareYBTabletPlacement(
	ctx context.Context,
	drill *restoreDrillCtx,
	container string,
	layout ybPlacementLayout,
	exportSnap string,
	inventories []ybArchiveInventory,
	remaps []ybTabletRemap,
	logger *slog.Logger,
) ([]ybTabletPlacement, error) {
	extractScript := ybTabletExtractScript(layout.ExportRoot, ybDrillArtifactMount)

	extractions := make([]ybNodeExtraction, 0, len(inventories))
	for _, inventory := range inventories {
		if extractRes, err := containerExec(
			ctx,
			drill.Cli,
			container,
			[]string{
				"sh",
				"-c",
				extractScript,
				"sh",
				inventory.Node,
			},
		); err != nil || extractRes.ExitCode != 0 {
			wrapped := fmt.Errorf(
				"extract tablets for node %s: exit %d: %w",
				inventory.Node,
				extractRes.ExitCode,
				err,
			)
			logger.ErrorContext(
				ctx,
				"backup.restore_drill.yb.failed",
				slog.String("err", wrapped.Error()),
			)

			return nil, wrapped
		}

		// A successful tar extraction reports only tar's result. The export inventory
		// supplies the independent record for checking each tablet below.
		missing, err := checkYBNodeExtraction(ctx, drill, container, layout.ExportRoot, inventory)
		if err != nil {
			return nil, err
		}

		extractions = append(extractions, newYBNodeExtraction(inventory, missing))
	}

	placements, defects := chooseYBTabletReplicas(
		remaps,
		layout.ExportRoot,
		exportSnap,
		extractions,
	)
	if len(defects) > 0 {
		wrapped := ybTabletDefectError(defects, countDistinctTablets(remaps))
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.yb.failed",
			slog.String("err", wrapped.Error()),
		)

		return nil, wrapped
	}

	return placements, nil
}

func restoreImportedYBSnapshot(
	ctx context.Context,
	drill *restoreDrillCtx,
	container string,
	master string,
	newSnap string,
	logger *slog.Logger,
) error {
	if restoreRes, err := containerExec(
		ctx,
		drill.Cli,
		container,
		[]string{
			ybAdminBinary,
			"--master_addresses",
			master,
			"restore_snapshot",
			newSnap,
		},
	); err != nil || restoreRes.ExitCode != 0 {
		wrapped := fmt.Errorf(
			"restore_snapshot exit %d: %s: %w",
			restoreRes.ExitCode,
			strings.TrimSpace(restoreRes.Stderr),
			err,
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.yb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	// Wait for restoration to finish before reading tables. Reads during
	// restoration return empty pre-restore rows that the row assertion would
	// misreport as data loss. Restoration time grows with the ledger. The wait
	// stops on a stall instead of a fixed duration. A master-reported restoration
	// failure ends the wait immediately.
	restoreWatch := newYBScratchWatch(
		drill,
		container,
		master,
		nil,
		[]string{
			"sh",
			"-c",
			ybAdminBinary + " --master_addresses " + master + " list_snapshot_restorations | grep -q RESTORED",
		},
	)
	took, err := awaitYBScratch(
		ctx,
		"snapshot restoration",
		restoreWatch,
		ybScratchStallWindow,
		ybScratchPollInterval,
		ybScratchProbeTimeout,
	)
	if err != nil {
		wrapped := fmt.Errorf("snapshot restoration did not reach RESTORED: %w", err)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.yb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	logger.InfoContext(ctx, "backup.restore_drill.yb.restored", slog.Duration("took", took))

	return nil
}
