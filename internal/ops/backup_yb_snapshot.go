// backup_yb_snapshot.go exports a YugabyteDB distributed snapshot off-host to
// the object store, the last-resort backup tier for the SQL store. Disaster
// recovery for YugabyteDB is quorum replication plus a standby cluster; the
// in-cluster snapshot schedule is the corruption layer and does not survive
// loss of the host's storage. This export does. See
// https://docs.yugabyte.com/stable/manage/backup-restore/snapshot-ysql/

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// ybSnapshotCompleteTimeout bounds the wait for create_database_snapshot to
	// reach COMPLETE. A single-node database snapshot completes in seconds; the
	// budget is generous to tolerate a busy node.
	ybSnapshotCompleteTimeout = 5 * time.Minute
	// ybSnapshotPollInterval is the gap between list_snapshots polls.
	ybSnapshotPollInterval = 3 * time.Second
)

// ybSnapshotStatus is the lifecycle state yb-admin reports for a snapshot.
type ybSnapshotStatus string

const (
	ybSnapStatusUnknown  ybSnapshotStatus = ""
	ybSnapStatusCreating ybSnapshotStatus = "CREATING"
	ybSnapStatusComplete ybSnapshotStatus = "COMPLETE"
	ybSnapStatusFailed   ybSnapshotStatus = "FAILED"
)

// RunBackupYBSnapshotExport orchestrates a YugabyteDB distributed-snapshot
// export: it creates the cluster snapshot, waits COMPLETE, and uploads the
// snapshot metadata, a schema-only ysql_dump, and a completeness manifest under
// one run key. Every helper runs in a one-shot container against the master
// quorum names; no local yugabyte container is required on the guest that runs
// this command. The tablet snapshot files stay on the data guests: each guest's
// `ops backup yb-archive-node` timer uploads its own node's tar under the
// prefix the manifest assigns it, and the restore drill refuses the run until
// every listed prefix is filled.
func RunBackupYBSnapshotExport(ctx context.Context, cfg *config.Config) error {
	logger := telemetry.L(ctx)
	if cfg.BackupS3Endpoint == "" || cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" {
		err := fmt.Errorf("yb-snapshot-export: TACK_BACKUP_S3_ENDPOINT, _ACCESS_KEY_ID, and _SECRET_ACCESS_KEY are required")
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", err.Error()))
		return err
	}
	if cfg.YugabytePassword == "" {
		err := fmt.Errorf("yb-snapshot-export: YUGABYTE_PASSWORD is required")
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", err.Error()))
		return err
	}

	filter := "ysql." + cfg.YugabyteDB
	runID := opsNow().UTC().Format(ybSnapshotRunIDLayout)
	stageDir := filepath.Join(cfg.BackupRoot, "yb-snapshot-"+runID)
	if err := os.MkdirAll(stageDir, 0o750); err != nil {
		wrapped := fmt.Errorf("mkdir yb snapshot stage %s: %w", stageDir, err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	// export_snapshot runs in a one-shot and writes the metadata file into this
	// dir via a bind mount, so it must be writable by the container user. The
	// dir is transient and removed after the upload succeeds.
	if err := os.Chmod(stageDir, 0o777); err != nil {
		wrapped := fmt.Errorf("chmod yb snapshot stage %s: %w", stageDir, err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	cli, err := newDockerClient(ctx)
	if err != nil {
		return err
	}
	defer cli.Close()

	logger.InfoContext(
		ctx, "backup.yb_snapshot.start",
		slog.String("filter", filter),
		slog.String("run_id", runID),
		slog.String("masters", cfg.BackupYBMasterAddresses),
	)

	// A prior run's in-cluster snapshot is only deletable once every node has
	// archived its tablet files, so cleanup happens here rather than at the end
	// of the run that created it. Best effort: a failed cleanup must not block
	// a new export.
	cleanupYBPriorExportSnapshots(ctx, cli, cfg)

	createRes, err := ybAdminOneShot(ctx, cli, cfg, nil, "create_database_snapshot", filter)
	if err != nil {
		return err
	}
	snapshotID := parseSnapshotID(createRes.Stdout)
	if snapshotID == "" {
		wrapped := fmt.Errorf("yb-snapshot-export: could not parse snapshot id from %q", strings.TrimSpace(createRes.Stdout))
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, "backup.yb_snapshot.created", slog.String("snapshot_id", snapshotID))

	if err := waitYBSnapshotComplete(ctx, cli, cfg, snapshotID); err != nil {
		return err
	}

	if err := exportYBSnapshotToObjectStore(ctx, cli, cfg, runID, stageDir, snapshotID); err != nil {
		return err
	}

	// The in-cluster snapshot must survive this command: the per-node archive
	// timers still read its tablet files from each data guest. The next export
	// run deletes it once the manifest is satisfied.

	if rmErr := os.RemoveAll(stageDir); rmErr != nil {
		logger.WarnContext(ctx, "backup.yb_snapshot.stage_cleanup_failed",
			slog.String("dir", stageDir), slog.String("err", rmErr.Error()))
	}

	logger.InfoContext(
		ctx, "backup.yb_snapshot.completed",
		slog.String("run_id", runID),
		slog.String("snapshot_id", snapshotID),
		slog.String("bucket", cfg.BackupS3BucketMain),
		slog.String("key_prefix", ybSnapshotKeyPrefix(runID)),
	)
	return nil
}

// exportYBSnapshotToObjectStore runs the post-creation pipeline for a snapshot
// already known COMPLETE: export the metadata file, dump the schema, write the
// completeness manifest from the live tablet-server list, and upload all three
// under the run's key prefix. The tablet archives themselves are each data
// guest's job (`ops backup yb-archive-node`); this pipeline only declares the
// node prefixes those archives must fill.
func exportYBSnapshotToObjectStore(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	runID, stageDir, snapshotID string,
) error {
	if _, err := ybAdminOneShot(ctx, cli, cfg,
		[]string{stageDir + ":/out"},
		"export_snapshot", snapshotID, "/out/"+ybSnapshotMetadataObject); err != nil {
		return err
	}

	schemaPath := filepath.Join(stageDir, "schema.sql")
	if err := dumpYBSchemaOneShot(ctx, cli, cfg, stageDir, schemaPath); err != nil {
		return err
	}

	nodes, err := listYBTabletServerNodes(ctx, cli, cfg)
	if err != nil {
		return err
	}

	manifest := newYBSnapshotManifest(runID, snapshotID, cfg.YugabyteDB, nodes)
	manifestPath := filepath.Join(stageDir, ybSnapshotManifestObject)
	if err := writeYBSnapshotManifest(ctx, manifestPath, manifest); err != nil {
		return err
	}

	// The manifest uploads last: archivers and the restore drill treat a
	// manifest-less run prefix as not yet published, so a failure part-way
	// through the uploads can never leave a manifest gating absent objects.
	return uploadYBSnapshotArtifacts(ctx, cfg, runID,
		ybSnapshotUploadArtifacts(stageDir, schemaPath, manifestPath))
}

// listYBTabletServerNodes derives the tablet-server node names live from
// yb-admin so the manifest tracks the cluster rather than a config value. An
// empty list is an error: a manifest that lists no nodes would gate nothing.
func listYBTabletServerNodes(ctx context.Context, cli *client.Client, cfg *config.Config) ([]string, error) {
	logger := telemetry.L(ctx)
	res, err := ybAdminOneShot(ctx, cli, cfg, nil, "list_all_tablet_servers")
	if err != nil {
		return nil, err
	}
	nodes := parseYBTabletServers(res.Stdout)
	if len(nodes) == 0 {
		wrapped := fmt.Errorf("yb-snapshot-export: list_all_tablet_servers reported no tablet servers: %q",
			strings.TrimSpace(res.Stdout))
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	logger.InfoContext(ctx, "backup.yb_snapshot.tablet_servers", slog.Any("nodes", nodes))
	return nodes, nil
}

// dumpYBSchemaOneShot writes a schema-only ysql_dump of the database into the
// bind-mounted stage dir, running ysql_dump in a one-shot container the same
// way yb-admin one-shots run and pointing it at the first master's node name.
// The export_snapshot metadata references table ids this schema recreates on
// import; --include-yb-metadata preserves the YugabyteDB table properties the
// import path needs.
func dumpYBSchemaOneShot(ctx context.Context, cli *client.Client, cfg *config.Config, stageDir, schemaPath string) error {
	logger := telemetry.L(ctx)
	host := ybFirstMasterHost(cfg.BackupYBMasterAddresses)
	res, err := runOneShot(ctx, cli, logger, runOneShotOptions{
		Image:      cfg.BackupYBPITRImage,
		Network:    cfg.BackupFDBNetwork,
		Entrypoint: []string{"/home/yugabyte/postgres/bin/ysql_dump"},
		Cmd: []string{
			"-h", host,
			"-p", "5433",
			"-U", cfg.YugabyteUser,
			"-d", cfg.YugabyteDB,
			"--schema-only",
			"--include-yb-metadata",
			"--no-owner",
			"--no-privileges",
			"-f", "/out/schema.sql",
		},
		Env:        []string{"PGPASSWORD=" + cfg.YugabytePassword},
		Binds:      []string{stageDir + ":/out"},
		ExtraHosts: nil,
		Name:       "",
	})
	if err != nil {
		wrapped := fmt.Errorf("ysql_dump schema one-shot: %w", err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.schema_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if res.ExitCode != 0 {
		wrapped := fmt.Errorf("ysql_dump schema exited %d: %s", res.ExitCode,
			strings.TrimSpace(res.Stdout+" "+res.Stderr))
		logger.ErrorContext(ctx, "backup.yb_snapshot.schema_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	info, err := os.Stat(schemaPath)
	if err != nil {
		wrapped := fmt.Errorf("stat %s: %w", schemaPath, err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.schema_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if info.Size() == 0 {
		wrapped := fmt.Errorf("ysql_dump schema produced 0 bytes; refuse to ship empty schema")
		logger.ErrorContext(ctx, "backup.yb_snapshot.schema_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, "backup.yb_snapshot.schema_dumped",
		slog.String("path", schemaPath), slog.Int64("bytes", info.Size()))
	return nil
}

// ybSnapshotCleanupMaxRuns bounds how many of the newest export run prefixes
// the pre-export cleanup examines. The archivers only ever fill the newest
// manifest, so a run still incomplete after this many newer exports will never
// complete; its snapshot then falls to the orphan reconcile instead of being
// tracked forever.
const ybSnapshotCleanupMaxRuns = 30

// cleanupYBPriorExportSnapshots reconciles the cluster's snapshots against the
// export run prefixes before a new export starts. Every walked prior run whose
// manifest is fully archived loses its in-cluster snapshot; runs still waiting
// on node archives keep theirs, because the lagging nodes' archive timers
// still need the tablet files on disk. Snapshots no walked manifest references
// (runs that failed before their manifest upload, which lands last) are
// deleted as orphans, except snapshots owned by the PITR snapshot schedule.
// The orphan pass only runs when the reference set is trustworthy: it is
// skipped when there are no run prefixes to walk or when any walked run's
// manifest failed to resolve (a missing manifest counts as resolved), because
// an incomplete reference set would misclassify a live run's snapshot as an
// orphan and delete it. Best effort throughout: any failure is logged and the
// new export proceeds, and every kept snapshot is logged with the reason.
func cleanupYBPriorExportSnapshots(ctx context.Context, cli *client.Client, cfg *config.Config) {
	logger := telemetry.L(ctx)
	s3Client := newBackupS3Client(cfg)
	runIDs, err := listYBSnapshotRunIDs(ctx, s3Client, cfg.BackupS3BucketMain)
	if err != nil {
		logger.WarnContext(ctx, "backup.yb_snapshot.cleanup_list_failed", slog.String("err", err.Error()))
		return
	}
	if len(runIDs) > ybSnapshotCleanupMaxRuns {
		runIDs = runIDs[len(runIDs)-ybSnapshotCleanupMaxRuns:]
	}
	states, err := listYBClusterSnapshots(ctx, cli, cfg)
	if err != nil {
		logger.WarnContext(ctx, "backup.yb_snapshot.cleanup_list_snapshots_failed", slog.String("err", err.Error()))
		return
	}
	referenced, orphanPassOK := cleanupYBExportRuns(ctx, runIDs, states,
		func(runID string) (ybSnapshotManifest, error) {
			return fetchYBSnapshotManifest(ctx, s3Client, cfg.BackupS3BucketMain, runID)
		},
		func(key string) (bool, error) {
			return objectExists(ctx, s3Client, cfg.BackupS3BucketMain, key)
		},
		func(snapshotID string) error {
			_, delErr := ybAdminOneShot(ctx, cli, cfg, nil, "delete_snapshot", snapshotID)
			return delErr
		},
	)
	if !orphanPassOK {
		return
	}
	cleanupYBOrphanSnapshots(ctx, cli, cfg, states, referenced)
}

// cleanupYBExportRuns walks the (already clamped) run prefixes, handling each
// through cleanupYBExportRun, and decides whether the orphan pass may run.
// orphanPassOK is false, with the reason logged, when there are no run
// prefixes to walk or when any walked run's manifest failed to resolve,
// because either way referenced cannot vouch for which cluster snapshots are
// orphans. The fetch, exists, and deleteSnapshot closures keep the walk
// testable without an object store or a cluster.
func cleanupYBExportRuns(
	ctx context.Context,
	runIDs []string,
	states map[string]ybSnapshotStatus,
	fetch func(runID string) (ybSnapshotManifest, error),
	exists func(key string) (bool, error),
	deleteSnapshot func(snapshotID string) error,
) (referenced map[string]bool, orphanPassOK bool) {
	logger := telemetry.L(ctx)
	referenced = map[string]bool{}
	if len(runIDs) == 0 {
		logger.InfoContext(ctx, "backup.yb_snapshot.cleanup_orphans_skipped",
			slog.String("reason", "no export run prefixes to build the reference set from"))
		return referenced, false
	}
	var unresolved []string
	for _, runID := range runIDs {
		if !cleanupYBExportRun(ctx, runID, states, referenced, fetch, exists, deleteSnapshot) {
			unresolved = append(unresolved, runID)
		}
	}
	if len(unresolved) > 0 {
		logger.InfoContext(ctx, "backup.yb_snapshot.cleanup_orphans_skipped",
			slog.String("reason", "manifests for runs "+strings.Join(unresolved, ", ")+
				" did not resolve, so the reference set is incomplete"))
		return referenced, false
	}
	return referenced, true
}

// cleanupYBExportRun handles one prior export run: it records the run's
// snapshot id in referenced so the orphan reconcile leaves it alone, deletes
// the in-cluster snapshot when every node prefix the manifest lists is filled,
// and logs why a kept snapshot survives. It reports whether the run's manifest
// resolved cleanly: a NotFound manifest (never uploaded) is clean, while any
// other fetch or parse failure is not, because that run's snapshot id then
// never reaches referenced and the orphan pass must not run.
func cleanupYBExportRun(
	ctx context.Context,
	runID string,
	states map[string]ybSnapshotStatus,
	referenced map[string]bool,
	fetch func(runID string) (ybSnapshotManifest, error),
	exists func(key string) (bool, error),
	deleteSnapshot func(snapshotID string) error,
) bool {
	logger := telemetry.L(ctx)
	manifest, err := fetch(runID)
	if err != nil {
		if isObjectNotFound(err) {
			logger.InfoContext(ctx, "backup.yb_snapshot.cleanup_run_skipped",
				slog.String("run_id", runID), slog.String("reason", "manifest not uploaded"))
			return true
		}
		logger.WarnContext(ctx, "backup.yb_snapshot.cleanup_manifest_failed",
			slog.String("run_id", runID), slog.String("err", err.Error()))
		return false
	}
	if manifest.SnapshotID == "" {
		return true
	}
	referenced[manifest.SnapshotID] = true
	state, inCluster := states[manifest.SnapshotID]
	if !inCluster {
		// Already deleted by an earlier cleanup; nothing to do for this run.
		return true
	}
	if len(manifest.Nodes) == 0 {
		logger.InfoContext(ctx, "backup.yb_snapshot.snapshot_kept",
			slog.String("run_id", runID), slog.String("snapshot_id", manifest.SnapshotID),
			slog.String("reason", "manifest lists no nodes"))
		return true
	}
	missing, err := missingYBNodeArchives(manifest, exists)
	if err != nil {
		logger.WarnContext(ctx, "backup.yb_snapshot.cleanup_check_failed",
			slog.String("run_id", runID), slog.String("err", err.Error()))
		return true
	}
	if len(missing) > 0 {
		logger.InfoContext(ctx, "backup.yb_snapshot.snapshot_kept",
			slog.String("run_id", runID), slog.String("snapshot_id", manifest.SnapshotID),
			slog.String("reason", "nodes "+strings.Join(missing, ", ")+" have not archived"))
		return true
	}
	if !ybSnapshotDeletable(state) {
		logger.InfoContext(ctx, "backup.yb_snapshot.snapshot_kept",
			slog.String("run_id", runID), slog.String("snapshot_id", manifest.SnapshotID),
			slog.String("reason", "state "+string(state)+" is not deletable"))
		return true
	}
	if delErr := deleteSnapshot(manifest.SnapshotID); delErr != nil {
		logger.WarnContext(ctx, "backup.yb_snapshot.delete_failed",
			slog.String("snapshot_id", manifest.SnapshotID), slog.String("err", delErr.Error()))
		return true
	}
	logger.InfoContext(ctx, "backup.yb_snapshot.prior_snapshot_deleted",
		slog.String("run_id", runID), slog.String("snapshot_id", manifest.SnapshotID))
	return true
}

// cleanupYBOrphanSnapshots deletes in-cluster snapshots that no walked export
// run manifest references, the leftovers of exports that failed after
// create_database_snapshot but before their manifest upload. Snapshots owned
// by the PITR snapshot schedule are never touched: the schedule retains them,
// not an export run. When the schedule listing fails the orphan pass is
// skipped entirely, because export orphans cannot be told apart from schedule
// snapshots without it.
func cleanupYBOrphanSnapshots(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	states map[string]ybSnapshotStatus,
	referenced map[string]bool,
) {
	logger := telemetry.L(ctx)
	unreferenced := false
	for id := range states {
		if !referenced[id] {
			unreferenced = true
			break
		}
	}
	if !unreferenced {
		return
	}
	scheduleOwned, err := listYBScheduleSnapshotIDs(ctx, cli, cfg)
	if err != nil {
		logger.WarnContext(ctx, "backup.yb_snapshot.cleanup_schedules_failed", slog.String("err", err.Error()))
		return
	}
	var kept []string
	var deleted []string
	for _, disposition := range reconcileYBOrphanSnapshots(states, referenced, scheduleOwned) {
		if !disposition.delete {
			kept = append(kept, disposition.id+": "+disposition.reason)
			continue
		}
		if _, delErr := ybAdminOneShot(ctx, cli, cfg, nil, "delete_snapshot", disposition.id); delErr != nil {
			logger.WarnContext(ctx, "backup.yb_snapshot.delete_failed",
				slog.String("snapshot_id", disposition.id), slog.String("err", delErr.Error()))
			continue
		}
		deleted = append(deleted, disposition.id)
	}
	if len(kept) > 0 {
		logger.InfoContext(ctx, "backup.yb_snapshot.snapshots_kept", slog.Any("snapshots", kept))
	}
	if len(deleted) > 0 {
		logger.InfoContext(ctx, "backup.yb_snapshot.orphan_snapshots_deleted",
			slog.Any("snapshot_ids", deleted),
			slog.String("reason", "no export run manifest references them"))
	}
}

// ybOrphanSnapshotDisposition is the reconcile verdict for one in-cluster
// snapshot that no walked export run manifest references.
type ybOrphanSnapshotDisposition struct {
	id     string
	delete bool
	reason string
}

// reconcileYBOrphanSnapshots classifies every cluster snapshot no walked run
// manifest references: schedule-owned snapshots and snapshots in a
// non-deletable state are kept with a reason, and the rest are orphans from
// exports that failed before their manifest upload. Sorted by id so the log
// order is deterministic.
func reconcileYBOrphanSnapshots(
	states map[string]ybSnapshotStatus,
	referenced, scheduleOwned map[string]bool,
) []ybOrphanSnapshotDisposition {
	ids := make([]string, 0, len(states))
	for id := range states {
		if !referenced[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	dispositions := make([]ybOrphanSnapshotDisposition, 0, len(ids))
	for _, id := range ids {
		switch {
		case scheduleOwned[id]:
			dispositions = append(dispositions, ybOrphanSnapshotDisposition{
				id: id, delete: false, reason: "owned by a snapshot schedule",
			})
		case !ybSnapshotDeletable(states[id]):
			dispositions = append(dispositions, ybOrphanSnapshotDisposition{
				id: id, delete: false, reason: "state " + string(states[id]) + " is not deletable",
			})
		default:
			dispositions = append(dispositions, ybOrphanSnapshotDisposition{
				id: id, delete: true, reason: "no export run manifest references it",
			})
		}
	}
	return dispositions
}

// ybSnapshotDeletable reports whether delete_snapshot can act on a snapshot in
// this state. CREATING snapshots are left for a later cleanup, and snapshots
// already DELETING or DELETED are going away on their own.
func ybSnapshotDeletable(state ybSnapshotStatus) bool {
	return state == ybSnapStatusComplete || state == ybSnapStatusFailed
}

// listYBClusterSnapshots returns every snapshot the cluster reports, id to
// state, via a list_snapshots one-shot.
func listYBClusterSnapshots(ctx context.Context, cli *client.Client, cfg *config.Config) (map[string]ybSnapshotStatus, error) {
	res, err := ybAdminOneShot(ctx, cli, cfg, nil, "list_snapshots")
	if err != nil {
		return nil, err
	}
	return parseYBClusterSnapshots(res.Stdout), nil
}

// parseYBClusterSnapshots parses list_snapshots output into id to state. Data
// rows start with the snapshot UUID and carry the state second; the
// restoration section that can follow the snapshot rows is ignored, because
// restoration ids are also UUIDs and must not be mistaken for snapshots.
func parseYBClusterSnapshots(stdout string) map[string]ybSnapshotStatus {
	states := map[string]ybSnapshotStatus{}
	for line := range strings.SplitSeq(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "Restoration" {
			break
		}
		if len(fields) < 2 || !ybUUIDPattern.MatchString(fields[0]) {
			continue
		}
		states[fields[0]] = ybSnapshotStatus(fields[1])
	}
	return states
}

// listYBScheduleSnapshotIDs returns the ids of every snapshot owned by a
// snapshot schedule (the PITR layer), via a list_snapshot_schedules one-shot.
func listYBScheduleSnapshotIDs(ctx context.Context, cli *client.Client, cfg *config.Config) (map[string]bool, error) {
	res, err := ybAdminOneShot(ctx, cli, cfg, nil, "list_snapshot_schedules")
	if err != nil {
		return nil, err
	}
	return unmarshalYBScheduleSnapshotIDs(ctx, res.Stdout)
}

// unmarshalYBScheduleSnapshotIDs decodes list_snapshot_schedules JSON output
// ({"schedules":[{"id":...,"snapshots":[{"id":...}]}]}) into the set of
// schedule-owned snapshot ids.
func unmarshalYBScheduleSnapshotIDs(ctx context.Context, stdout string) (map[string]bool, error) {
	var parsed struct {
		Schedules []struct {
			Snapshots []struct {
				ID string `json:"id"`
			} `json:"snapshots"`
		} `json:"schedules"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &parsed); err != nil {
		wrapped := fmt.Errorf("parse list_snapshot_schedules output: %w", err)
		telemetry.L(ctx).WarnContext(ctx, "backup.yb_snapshot.schedules_unparseable",
			slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	owned := map[string]bool{}
	for _, schedule := range parsed.Schedules {
		for _, snapshot := range schedule.Snapshots {
			owned[snapshot.ID] = true
		}
	}
	return owned, nil
}

// ybAdminOneShot runs one yb-admin subcommand in a one-shot container on the
// tack compose network, prepending the master_addresses flag. binds is optional
// (export_snapshot needs the staging dir bound to write its metadata file). It
// returns an error on a non-zero exit so callers do not inspect codes.
func ybAdminOneShot(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	binds []string,
	args ...string,
) (execResult, error) {
	logger := telemetry.L(ctx)
	cmd := append([]string{"--master_addresses", cfg.BackupYBMasterAddresses}, args...)
	res, err := runOneShot(ctx, cli, logger, runOneShotOptions{
		Image:      cfg.BackupYBPITRImage,
		Network:    cfg.BackupFDBNetwork,
		Entrypoint: []string{ybAdminBinary},
		Cmd:        cmd,
		Env:        nil,
		Binds:      binds,
		ExtraHosts: nil,
		Name:       "",
	})
	if err != nil {
		wrapped := fmt.Errorf("yb-admin %s: %w", args[0], err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.yb_admin_failed",
			slog.String("subcommand", args[0]), slog.String("err", wrapped.Error()))
		return res, wrapped
	}
	if res.ExitCode != 0 {
		wrapped := fmt.Errorf("yb-admin %s exited %d: %s", args[0], res.ExitCode,
			strings.TrimSpace(res.Stdout+" "+res.Stderr))
		logger.ErrorContext(ctx, "backup.yb_snapshot.yb_admin_failed",
			slog.String("subcommand", args[0]), slog.String("err", wrapped.Error()))
		return res, wrapped
	}
	return res, nil
}

// parseSnapshotID extracts the snapshot UUID from create_database_snapshot
// output, which prints "Started snapshot creation: <uuid>". An empty return
// means the marker was absent, which the caller treats as a failure.
func parseSnapshotID(stdout string) string {
	_, after, found := strings.Cut(stdout, "creation:")
	if !found {
		return ""
	}
	fields := strings.Fields(after)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// waitYBSnapshotComplete polls list_snapshots until the snapshot reaches
// COMPLETE, errors on FAILED, and times out via context so the poll loop never
// consults the wall clock directly.
func waitYBSnapshotComplete(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	snapshotID string,
) error {
	logger := telemetry.L(ctx)
	pollCtx, cancel := context.WithTimeout(ctx, ybSnapshotCompleteTimeout)
	defer cancel()
	ticker := time.NewTicker(ybSnapshotPollInterval)
	defer ticker.Stop()
	for {
		res, err := ybAdminOneShot(pollCtx, cli, cfg, nil, "list_snapshots")
		if err == nil {
			switch ybSnapshotState(res.Stdout, snapshotID) {
			case ybSnapStatusComplete:
				return nil
			case ybSnapStatusFailed:
				wrapped := fmt.Errorf("yb-snapshot-export: snapshot %s reported FAILED", snapshotID)
				logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))
				return wrapped
			case ybSnapStatusUnknown, ybSnapStatusCreating:
				// Not ready yet; fall through to the wait below.
			}
		}
		select {
		case <-pollCtx.Done():
			wrapped := fmt.Errorf("yb-snapshot-export: snapshot %s did not reach COMPLETE within %s: %w",
				snapshotID, ybSnapshotCompleteTimeout, pollCtx.Err())
			logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))
			return wrapped
		case <-ticker.C:
		}
	}
}

// ybSnapshotState returns the status on the list_snapshots line that names
// snapshotID, or ybSnapStatusUnknown if the snapshot is not listed yet.
func ybSnapshotState(listOutput, snapshotID string) ybSnapshotStatus {
	for line := range strings.SplitSeq(listOutput, "\n") {
		if !strings.Contains(line, snapshotID) {
			continue
		}
		switch {
		case strings.Contains(line, string(ybSnapStatusComplete)):
			return ybSnapStatusComplete
		case strings.Contains(line, string(ybSnapStatusFailed)):
			return ybSnapStatusFailed
		default:
			return ybSnapStatusCreating
		}
	}
	return ybSnapStatusUnknown
}
