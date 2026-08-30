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

// ybTabletRemap is one row of the import_snapshot mapping: the table id is
// preserved while the tablet id is reassigned, so the export's tablet files
// (named by the old tablet id) are copied into the new tablet's snapshot dir.
type ybTabletRemap struct {
	table string
	old   string
	new   string
}

var ybUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ybDrillRestoreWaitTimeout bounds the wait for restore_snapshot to reach
// RESTORED on the scratch node. Warm, 130 tablets restore in ~20s; the budget
// covers a cold page cache right after a deploy, where the same restoration
// exceeded 90s (observed 2026-08-29 on the QA owner).
const ybDrillRestoreWaitTimeout = 5 * time.Minute

// importAndRestoreYBSnapshot imports the exported snapshot metadata, copies the
// exported tablet files into the new tablets the import created, and runs
// restore_snapshot. The schema must already be applied so the tables exist.
// nodes are the manifest's tablet-server names; each node's archive is staged
// as /artifacts/tablets-<name>.tar.gz and extracted into its own directory so
// replicas of the same tablet from different nodes never mix files.
func importAndRestoreYBSnapshot(ctx context.Context, r *restoreDrillCtx, container, database, exportSnap string, nodes []string) error {
	logger := telemetry.L(ctx)
	master := container + ":7100"

	importRes, err := containerExec(ctx, r.Cli, container,
		[]string{ybAdminBinary, "--master_addresses", master, "import_snapshot", "/artifacts/metadata.snapshot", database})
	if err != nil {
		return fmt.Errorf("import_snapshot exec: %w", err)
	}
	if importRes.ExitCode != 0 {
		wrapped := fmt.Errorf("import_snapshot exited %d: %s", importRes.ExitCode, strings.TrimSpace(importRes.Stderr))
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	remaps := parseYBSnapshotMapping(importRes.Stdout)
	if len(remaps) == 0 {
		wrapped := fmt.Errorf("import_snapshot produced no tablet mapping")
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	newSnap, err := newestYBSnapshotID(ctx, r, container, master)
	if err != nil {
		return err
	}

	// The extraction program is constant shell text; the node name arrives as
	// a positional argument ($1), so a manifest-supplied name can never be
	// parsed as shell syntax. Manifest decode also allowlists the names.
	const extractScript = `mkdir -p "/tmp/exp/$1" && tar xzf "/artifacts/tablets-$1.tar.gz" -C "/tmp/exp/$1"`
	for _, node := range nodes {
		if extractRes, err := containerExec(ctx, r.Cli, container,
			[]string{"sh", "-c", extractScript, "sh", node}); err != nil || extractRes.ExitCode != 0 {
			wrapped := fmt.Errorf("extract tablets for node %s: exit %d: %w", node, extractRes.ExitCode, err)
			logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
			return wrapped
		}
	}

	for _, script := range ybPlacementScripts(remaps, r.Cfg.BackupYBRocksDBDir, exportSnap, newSnap) {
		if placeRes, err := containerExec(ctx, r.Cli, container, []string{"sh", "-c", script}); err != nil || placeRes.ExitCode != 0 {
			wrapped := fmt.Errorf("place tablet files: exit %d: %s: %w", placeRes.ExitCode, strings.TrimSpace(placeRes.Stderr), err)
			logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
			return wrapped
		}
	}
	logger.InfoContext(ctx, "backup.restore_drill.yb.tablets_placed",
		slog.Int("tablets", len(remaps)), slog.String("new_snapshot_id", newSnap))

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

// ybPlacementChunkSize is how many tablet remaps one placement script carries.
// The script travels to the container as a single `sh -c` argument, and Linux
// caps one exec argument at 128KiB (MAX_ARG_STRLEN); one remap clause is about
// 300 bytes, so the 2026-08-30 QA drill broke that cap once the recreated
// corpus passed ~440 tablets. 128 remaps keeps a chunk near 40KiB.
const ybPlacementChunkSize = 128

// ybPlacementScripts builds the shell scripts that copy each exported tablet's
// files into the tablet the import created, split into bounded chunks so no
// script exceeds the kernel's per-argument limit however many tablets the
// corpus holds.
func ybPlacementScripts(remaps []ybTabletRemap, rocksdbDir, exportSnap, newSnap string) []string {
	var scripts []string
	for start := 0; start < len(remaps); start += ybPlacementChunkSize {
		end := min(start+ybPlacementChunkSize, len(remaps))
		var script strings.Builder
		script.WriteString("set -e; ")
		for _, m := range remaps[start:end] {
			// The glob spans the per-node extraction dirs; with replication the
			// same tablet exists under several nodes and any single replica's
			// file set is a consistent copy, so the first match wins and the
			// loop breaks before another node's replica can mix in.
			srcGlob := fmt.Sprintf("/tmp/exp/*/table-%s/tablet-%s.snapshots/%s", m.table, m.old, exportSnap)
			dst := fmt.Sprintf("%s/table-%s/tablet-%s.snapshots/%s", rocksdbDir, m.table, m.new, newSnap)
			// cp -a preserves the tablet files' ownership from the export, and
			// the placement exec runs as the container's default user (root in
			// the yugabyted image), matching the rocksdb files yugabyted reads.
			// No chown is needed, and the image has no `yugabyte` user name to
			// chown to.
			fmt.Fprintf(&script,
				"for src in %s; do if [ -d \"$src\" ]; then mkdir -p %q && cp -a \"$src\"/. %q/; break; fi; done; ",
				srcGlob, dst, dst)
		}
		scripts = append(scripts, script.String())
	}
	return scripts
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

// parseYBSnapshotMapping extracts the table-id-preserved, tablet-id-remapped
// rows from import_snapshot output. The mapping section begins at the line
// starting with "Object"; a "Table" row sets the current table id and each
// following "Tablet" row pairs an old tablet id with its new one.
func parseYBSnapshotMapping(out string) []ybTabletRemap {
	var remaps []ybTabletRemap
	started := false
	table := ""
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch {
		case fields[0] == "Object":
			started = true
		case !started:
			continue
		case fields[0] == "Table" && len(fields) >= 2:
			table = fields[1]
		case fields[0] == "Tablet" && len(fields) >= 4 && table != "":
			remaps = append(remaps, ybTabletRemap{table: table, old: fields[2], new: fields[3]})
		}
	}
	return remaps
}
