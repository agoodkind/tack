// YugabyteDB snapshot exports store SQL backups outside the host in object
// storage. This backup tier provides a last resort after host storage loss.
// Quorum replication and a standby cluster provide disaster recovery; the
// in-cluster snapshot schedule addresses corruption but depends on host
// storage.
//
// The [YugabyteDB snapshot reference] explains YSQL snapshot backup and
// restore.
//
// [YugabyteDB snapshot reference]: https://docs.yugabyte.com/stable/manage/backup-restore/snapshot-ysql/

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
	// ybSnapshotCompleteTimeout limits the wait for create_database_snapshot to
	// report COMPLETE.
	//
	// The original budget used these expectations:
	//   - A single-node database snapshot would complete within seconds.
	//   - A busy node could require more time.
	ybSnapshotCompleteTimeout = 5 * time.Minute
	// ybSnapshotPollInterval is the interval between list_snapshots polls.
	ybSnapshotPollInterval = 3 * time.Second
)

// ybSnapshotStatus represents the lifecycle state that yb-admin reports for a
// snapshot.
type ybSnapshotStatus string

const (
	ybSnapStatusUnknown  ybSnapshotStatus = ""
	ybSnapStatusCreating ybSnapshotStatus = "CREATING"
	ybSnapStatusComplete ybSnapshotStatus = "COMPLETE"
	ybSnapStatusFailed   ybSnapshotStatus = "FAILED"
)

// RunBackupYBSnapshotExport creates a YugabyteDB cluster snapshot, waits for
// COMPLETE, and uploads its metadata, schema-only ysql_dump, roles dump, and
// completeness manifest under one run key.
//
// Helpers use one-shot containers and master quorum hostnames. The command does
// not require a local YugabyteDB container. Tablet files remain on data guests
// until each guest's archive timer uploads its node's archive under the
// manifest's assigned prefix.
//
// The operation requires these settings:
//   - Object-store endpoint and credentials authorize backup uploads.
//   - The migrator password authorizes schema and roles dumps.
//
// Each data guest runs ops backup yb-archive-node. The restore drill rejects a
// run until every listed node has its required artifacts.
func RunBackupYBSnapshotExport(ctx context.Context, cfg *config.Config) error {
	logger := telemetry.L(ctx)
	configErr := checkYBExportConfig(ctx, cfg, logger)
	if configErr != nil {
		return configErr
	}

	filter := "ysql." + cfg.YugabyteDB
	runID := opsNow().UTC().Format(ybSnapshotRunIDLayout)
	stageDir := filepath.Join(cfg.BackupRoot, "yb-snapshot-"+runID)
	if err := os.MkdirAll(stageDir, 0o750); err != nil {
		wrapped := fmt.Errorf("mkdir yb snapshot stage %s: %w", stageDir, err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))

		return wrapped
	}

	// export_snapshot writes metadata through the staging directory's bind mount.
	// The container user needs write access to that temporary directory. Cleanup
	// removes it after a successful upload.
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
		ctx,
		"backup.yb_snapshot.start",
		slog.String("filter", filter),
		slog.String("run_id", runID),
		slog.String("masters", cfg.BackupYBMasterAddresses),
	)

	// Cleanup runs before the next export because prior snapshots must remain until
	// every node archives its tablet files. Cleanup logs failures without blocking
	// a new export.
	cleanupYBPriorExportSnapshots(ctx, cli, cfg)

	createRes, err := ybAdminOneShot(ctx, cli, cfg, nil, "create_database_snapshot", filter)
	if err != nil {
		return err
	}

	snapshotID := parseSnapshotID(createRes.Stdout)
	if snapshotID == "" {
		wrapped := fmt.Errorf(
			"yb-snapshot-export: could not parse snapshot id from %q",
			strings.TrimSpace(createRes.Stdout),
		)
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))

		return wrapped
	}

	logger.InfoContext(ctx, "backup.yb_snapshot.created", slog.String("snapshot_id", snapshotID))

	if err := waitYBSnapshotComplete(ctx, cli, cfg, snapshotID); err != nil {
		return err
	}

	if err := exportYBSnapshotToObjectStore(
		ctx,
		cli,
		cfg,
		runID,
		stageDir,
		snapshotID,
	); err != nil {
		return err
	}

	// The per-node archive timers still need each data guest's tablet files after
	// this command completes. A later export deletes the snapshot after every node
	// supplies the artifacts its manifest requires.

	if rmErr := os.RemoveAll(stageDir); rmErr != nil {
		logger.WarnContext(
			ctx,
			"backup.yb_snapshot.stage_cleanup_failed",
			slog.String("dir", stageDir),
			slog.String("err", rmErr.Error()),
		)
	}

	logger.InfoContext(
		ctx,
		"backup.yb_snapshot.completed",
		slog.String("run_id", runID),
		slog.String("snapshot_id", snapshotID),
		slog.String("bucket", cfg.BackupS3BucketMain),
		slog.String("key_prefix", ybSnapshotKeyPrefix(runID)),
	)

	return nil
}

// exportYBSnapshotToObjectStore processes a snapshot after it reports COMPLETE.
// It exports metadata, dumps the schema and cluster roles, builds the manifest
// from the live tablet-server list, and uploads artifacts under the run prefix.
// Each data guest uploads its own tablet archive with ops backup
// yb-archive-node; this function defines the required node prefixes.
func exportYBSnapshotToObjectStore(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	runID string,
	stageDir string,
	snapshotID string,
) error {
	if _, err := ybAdminOneShot(
		ctx,
		cli,
		cfg,
		[]string{stageDir + ":" + ybDumpOutDir},
		"export_snapshot",
		snapshotID,
		ybDumpOutDir+"/"+ybSnapshotMetadataObject,
	); err != nil {
		return err
	}

	schemaPath := filepath.Join(stageDir, ybSnapshotSchemaObject)
	if err := dumpYBSchemaOneShot(ctx, cli, cfg, stageDir, schemaPath); err != nil {
		return err
	}

	rolesPath := filepath.Join(stageDir, ybSnapshotRolesObject)
	if err := dumpYBRolesOneShot(ctx, cli, cfg, stageDir, rolesPath); err != nil {
		return err
	}

	nodes, err := listYBTabletServerNodes(ctx, cli, cfg)
	if err != nil {
		return err
	}

	manifestPath := filepath.Join(stageDir, ybSnapshotManifestObject)
	artifacts := ybSnapshotUploadArtifacts(stageDir, schemaPath, rolesPath, manifestPath)
	manifest := newYBSnapshotManifest(
		runID,
		snapshotID,
		cfg.YugabyteDB,
		nodes,
		ybSnapshotGatedArtifactNames(artifacts),
	)
	if err := writeYBSnapshotManifest(ctx, manifestPath, manifest); err != nil {
		return err
	}

	// Archivers and the restore drill discover published runs through their
	// manifests. The uploader publishes the manifest last, after the objects it
	// declares. A payload upload failure stops publication before the manifest.
	return uploadYBSnapshotArtifacts(ctx, cfg, ybSnapshotKeyPrefix(runID), artifacts)
}

// listYBTabletServerNodes reads current tablet-server hostnames from yb-admin
// instead of configuration. It rejects an empty list because the manifest must
// require node archives.
func listYBTabletServerNodes(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
) ([]string, error) {
	logger := telemetry.L(ctx)
	res, err := ybAdminOneShot(ctx, cli, cfg, nil, "list_all_tablet_servers")
	if err != nil {
		return nil, err
	}

	nodes := parseYBTabletServers(res.Stdout)
	if len(nodes) == 0 {
		wrapped := fmt.Errorf(
			"yb-snapshot-export: list_all_tablet_servers reported no tablet servers: %q",
			strings.TrimSpace(res.Stdout),
		)
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))

		return nil, wrapped
	}

	logger.InfoContext(ctx, "backup.yb_snapshot.tablet_servers", slog.Any("nodes", nodes))

	return nodes, nil
}

// ybSnapshotCleanupMaxRuns limits cleanup to the newest export run prefixes.
// Archivers process only the newest manifest. A run that remains incomplete
// beyond this window cannot complete through those timers; orphan cleanup
// examines its snapshot instead.
const ybSnapshotCleanupMaxRuns = 30

// cleanupYBPriorExportSnapshots compares cluster snapshots with export run
// prefixes before starting a new export. It deletes a prior run's snapshot
// after every node archives its files and retains snapshots that lagging node
// timers still need.
//
// Orphan cleanup deletes unreferenced export snapshots, including failures
// before manifest upload, and preserves snapshots owned by the point-in-time
// recovery schedule. It requires a nonempty run listing and successfully
// resolved manifests for every examined run. A missing manifest counts as
// resolved. An incomplete reference set could misclassify a live export
// snapshot.
//
// Cleanup logs failures without blocking the new export and logs reasons for
// retained snapshots.
func cleanupYBPriorExportSnapshots(ctx context.Context, cli *client.Client, cfg *config.Config) {
	logger := telemetry.L(ctx)
	s3Client := newBackupS3Client(cfg)
	runIDs, err := listYBSnapshotRunIDs(ctx, s3Client, cfg.BackupS3BucketMain)
	if err != nil {
		logger.WarnContext(
			ctx,
			"backup.yb_snapshot.cleanup_list_failed",
			slog.String("err", err.Error()),
		)

		return
	}

	if len(runIDs) > ybSnapshotCleanupMaxRuns {
		runIDs = runIDs[len(runIDs)-ybSnapshotCleanupMaxRuns:]
	}

	states, err := listYBClusterSnapshots(ctx, cli, cfg)
	if err != nil {
		logger.WarnContext(
			ctx,
			"backup.yb_snapshot.cleanup_list_snapshots_failed",
			slog.String("err", err.Error()),
		)

		return
	}

	referenced, orphanPassOK := cleanupYBExportRuns(
		ctx,
		runIDs,
		states,
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

// cleanupYBExportRuns processes the caller-limited run prefixes with
// cleanupYBExportRun and determines whether orphan cleanup may run. It logs a
// reason and returns orphanPassOK as false for an empty run listing or an
// unresolved manifest. Either case prevents a complete reference set.
// Caller-supplied fetch, exists, and deleteSnapshot functions allow tests
// without object storage or a cluster.
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
		logger.InfoContext(
			ctx,
			"backup.yb_snapshot.cleanup_orphans_skipped",
			slog.String("reason", "no export run prefixes to build the reference set from"),
		)

		return referenced, false
	}

	var unresolved []string

	for _, runID := range runIDs {
		if !cleanupYBExportRun(ctx, runID, states, referenced, fetch, exists, deleteSnapshot) {
			unresolved = append(unresolved, runID)
		}
	}

	if len(unresolved) > 0 {
		logger.InfoContext(
			ctx,
			"backup.yb_snapshot.cleanup_orphans_skipped",
			slog.String(
				"reason",
				"manifests for runs "+strings.Join(unresolved, ", ")+
					" did not resolve, so the reference set is incomplete",
			),
		)

		return referenced, false
	}

	return referenced, true
}

// cleanupYBExportRun records a prior run's snapshot ID in referenced before
// evaluating deletion. It deletes a snapshot only after all node artifacts
// exist and logs reasons for retention. A NotFound manifest counts as resolved.
// Other fetch or parse failures leave the snapshot ID unknown and prevent
// orphan cleanup.
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
			logger.InfoContext(
				ctx,
				"backup.yb_snapshot.cleanup_run_skipped",
				slog.String("run_id", runID),
				slog.String("reason", "manifest not uploaded"),
			)

			return true
		}

		logger.WarnContext(
			ctx,
			"backup.yb_snapshot.cleanup_manifest_failed",
			slog.String("run_id", runID),
			slog.String("err", err.Error()),
		)

		return false
	}

	if manifest.SnapshotID == "" {
		return true
	}

	referenced[manifest.SnapshotID] = true
	state, inCluster := states[manifest.SnapshotID]
	if !inCluster {
		// The cluster does not list this snapshot, which an earlier cleanup may already
		// have deleted. This run does not require further cleanup.
		return true
	}

	if len(manifest.Nodes) == 0 {
		logKeptYBSnapshot(ctx, logger, runID, manifest.SnapshotID, "manifest lists no nodes")

		return true
	}

	missing, err := missingYBNodeArchives(manifest, exists)
	if err != nil {
		logger.WarnContext(
			ctx,
			"backup.yb_snapshot.cleanup_check_failed",
			slog.String("run_id", runID),
			slog.String("err", err.Error()),
		)

		return true
	}

	if len(missing) > 0 {
		logKeptYBSnapshot(
			ctx,
			logger,
			runID,
			manifest.SnapshotID,
			"nodes "+strings.Join(missing, ", ")+" have not archived",
		)

		return true
	}

	if !ybSnapshotDeletable(state) {
		logKeptYBSnapshot(
			ctx,
			logger,
			runID,
			manifest.SnapshotID,
			"state "+string(state)+" is not deletable",
		)

		return true
	}

	if delErr := deleteSnapshot(manifest.SnapshotID); delErr != nil {
		logger.WarnContext(
			ctx,
			"backup.yb_snapshot.delete_failed",
			slog.String("snapshot_id", manifest.SnapshotID),
			slog.String("err", delErr.Error()),
		)

		return true
	}

	logDeletedYBSnapshot(ctx, logger, runID, manifest.SnapshotID)

	return true
}

// cleanupYBOrphanSnapshots deletes eligible in-cluster snapshots that examined
// export manifests do not reference. Failed exports can leave these snapshots
// after create_database_snapshot and before manifest upload. The point-in-time
// recovery schedule manages its own snapshots, which this cleanup preserves. A
// failed schedule listing prevents orphan cleanup because it cannot determine
// schedule ownership.
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
		logger.WarnContext(
			ctx,
			"backup.yb_snapshot.cleanup_schedules_failed",
			slog.String("err", err.Error()),
		)

		return
	}

	var kept []string
	var deleted []string

	for _, disposition := range reconcileYBOrphanSnapshots(states, referenced, scheduleOwned) {
		if !disposition.delete {
			kept = append(kept, disposition.id+": "+disposition.reason)
			continue
		}

		if _, delErr := ybAdminOneShot(
			ctx,
			cli,
			cfg,
			nil,
			"delete_snapshot",
			disposition.id,
		); delErr != nil {
			logger.WarnContext(
				ctx,
				"backup.yb_snapshot.delete_failed",
				slog.String("snapshot_id", disposition.id),
				slog.String("err", delErr.Error()),
			)
			continue
		}

		deleted = append(deleted, disposition.id)
	}

	if len(kept) > 0 {
		logger.InfoContext(ctx, "backup.yb_snapshot.snapshots_kept", slog.Any("snapshots", kept))
	}

	if len(deleted) > 0 {
		logger.InfoContext(
			ctx,
			"backup.yb_snapshot.orphan_snapshots_deleted",
			slog.Any("snapshot_ids", deleted),
			slog.String("reason", "no export run manifest references them"),
		)
	}
}

// ybOrphanSnapshotDisposition records a cleanup decision for an in-cluster
// snapshot that examined export manifests do not reference.
type ybOrphanSnapshotDisposition struct {
	id     string
	delete bool
	reason string
}

// reconcileYBOrphanSnapshots classifies snapshots that examined run manifests
// do not reference. It retains schedule-owned snapshots and states that do not
// permit deletion, with a reason. It marks the remaining snapshots for deletion
// as possible failed-export leftovers. Results sort by ID for stable log order.
func reconcileYBOrphanSnapshots(
	states map[string]ybSnapshotStatus,
	referenced map[string]bool,
	scheduleOwned map[string]bool,
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
			dispositions = append(
				dispositions,
				ybOrphanSnapshotDisposition{
					id:     id,
					delete: false,
					reason: "owned by a snapshot schedule",
				},
			)
		case !ybSnapshotDeletable(states[id]):
			dispositions = append(
				dispositions,
				ybOrphanSnapshotDisposition{
					id:     id,
					delete: false,
					reason: "state " + string(states[id]) + " is not deletable",
				},
			)
		default:
			dispositions = append(
				dispositions,
				ybOrphanSnapshotDisposition{
					id:     id,
					delete: true,
					reason: "no export run manifest references it",
				},
			)
		}
	}

	return dispositions
}

// ybSnapshotDeletable reports whether delete_snapshot can operate on the state.
// Cleanup leaves CREATING snapshots for a later attempt and does not retry
// snapshots already DELETING or DELETED.
func ybSnapshotDeletable(state ybSnapshotStatus) bool {
	return state == ybSnapStatusComplete || state == ybSnapStatusFailed
}

// listYBClusterSnapshots runs list_snapshots in a one-shot container and
// returns snapshot IDs with their states.
func listYBClusterSnapshots(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
) (map[string]ybSnapshotStatus, error) {
	res, err := ybAdminOneShot(ctx, cli, cfg, nil, "list_snapshots")
	if err != nil {
		return nil, err
	}

	return parseYBClusterSnapshots(res.Stdout), nil
}

// parseYBClusterSnapshots parses list_snapshots rows into snapshot IDs and
// states. A data row starts with a snapshot UUID followed by its state. The
// parser stops at the restoration section to avoid treating restoration UUIDs
// as snapshots.
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

// listYBScheduleSnapshotIDs runs list_snapshot_schedules in a one-shot
// container and returns the snapshot IDs owned by point-in-time recovery
// schedules.
func listYBScheduleSnapshotIDs(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
) (map[string]bool, error) {
	res, err := ybAdminOneShot(ctx, cli, cfg, nil, "list_snapshot_schedules")
	if err != nil {
		return nil, err
	}

	return unmarshalYBScheduleSnapshotIDs(ctx, res.Stdout)
}

// unmarshalYBScheduleSnapshotIDs decodes list_snapshot_schedules JSON into
// schedule-owned snapshot IDs. The output uses
// {"schedules":[{"id":...,"snapshots":[{"id":...}]}]}.
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
		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.yb_snapshot.schedules_unparseable",
			slog.String("err", wrapped.Error()),
		)

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

// ybAdminClusterAccess returns master-address flags and optional certificate
// flags and mounts for a one-shot container. An encrypted cluster with a
// configured certificate directory also uses that directory, following
// TACK-460.
//
// yb-admin uses the cluster protocol rather than SQL. It accepts the
// certificate directory as a flag instead of a connection string and reads the
// same directory as the engine. The read-only mount preserves the host path in
// flags and logs.
//
// When LedgerTLSEnabled is true and ledgerCertsDir returns an empty string,
// this helper returns only master-address flags. The command must report any
// resulting connection failure.
func ybAdminClusterAccess(cfg *config.Config) (args []string, binds []string) {
	args = []string{"--master_addresses", cfg.BackupYBMasterAddresses}
	certsDir := ledgerCertsDir(cfg)
	if !cfg.LedgerTLSEnabled || certsDir == "" {
		return args, nil
	}

	args = append(args, "--certs_dir_name", certsDir)

	return args, []string{certsDir + ":" + certsDir + ":ro"}
}

// ledgerCertsDir returns the configured certificate directory after trimming
// surrounding whitespace. Environment files can obscure stray spaces. Callers
// treat a blank result as an unset directory rather than a path made of spaces.
func ledgerCertsDir(cfg *config.Config) string {
	return strings.TrimSpace(cfg.LedgerCertsDir)
}

// ybAdminOneShot runs a yb-admin subcommand in a one-shot container on the Tack
// Compose network with cluster-access flags first. Optional binds provide
// mounts, including the export_snapshot staging directory for metadata. The
// helper returns an error for a nonzero exit status; callers do not need to
// inspect that status.
func ybAdminOneShot(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	binds []string,
	args ...string,
) (execResult, error) {
	logger := telemetry.L(ctx)
	accessArgs, accessBinds := ybAdminClusterAccess(cfg)
	cmd := make([]string, 0, len(accessArgs)+len(args))
	cmd = append(cmd, accessArgs...)
	cmd = append(cmd, args...)
	mounts := make([]string, 0, len(accessBinds)+len(binds))
	mounts = append(mounts, accessBinds...)
	mounts = append(mounts, binds...)
	user := ""
	if len(binds) > 0 {
		user = stageWriterUser()
	}

	res, err := runOneShot(
		ctx,
		cli,
		logger,
		runOneShotOptions{
			Image:      cfg.BackupYBImage,
			Network:    cfg.BackupFDBNetwork,
			Entrypoint: []string{ybAdminBinary},
			Cmd:        cmd,
			Env:        nil,
			Binds:      mounts,
			ExtraHosts: nil,
			Name:       "",
			User:       user,
		},
	)
	if err != nil {
		wrapped := fmt.Errorf("yb-admin %s: %w", args[0], err)
		logger.ErrorContext(
			ctx,
			"backup.yb_snapshot.yb_admin_failed",
			slog.String("subcommand", args[0]),
			slog.String("err", wrapped.Error()),
		)

		return res, wrapped
	}

	if res.ExitCode != 0 {
		wrapped := fmt.Errorf(
			"yb-admin %s exited %d: %s",
			args[0],
			res.ExitCode,
			strings.TrimSpace(res.Stdout+" "+res.Stderr),
		)
		logger.ErrorContext(
			ctx,
			"backup.yb_snapshot.yb_admin_failed",
			slog.String("subcommand", args[0]),
			slog.String("err", wrapped.Error()),
		)

		return res, wrapped
	}

	return res, nil
}

// parseSnapshotID extracts the snapshot UUID from create_database_snapshot
// output such as "Started snapshot creation: <uuid>". It returns an empty
// string when the creation: marker or a following field is missing. The caller
// treats an empty result as failure.
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

// waitYBSnapshotComplete polls list_snapshots until the snapshot reports
// COMPLETE or FAILED. It returns an error for FAILED and uses a context
// deadline for timeout instead of reading the wall clock directly.
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
				logger.ErrorContext(
					ctx,
					"backup.yb_snapshot.failed",
					slog.String("err", wrapped.Error()),
				)

				return wrapped
			case ybSnapStatusUnknown, ybSnapStatusCreating:
				// The snapshot is not ready. The loop waits for the next poll or context
				// cancellation.
			}
		}

		select {
		case <-pollCtx.Done():
			wrapped := fmt.Errorf(
				"yb-snapshot-export: snapshot %s did not reach COMPLETE within %s: %w",
				snapshotID,
				ybSnapshotCompleteTimeout,
				pollCtx.Err(),
			)
			logger.ErrorContext(
				ctx,
				"backup.yb_snapshot.failed",
				slog.String("err", wrapped.Error()),
			)

			return wrapped
		case <-ticker.C:
		}
	}
}

// ybSnapshotState returns the state from a list_snapshots line containing
// snapshotID. It returns ybSnapStatusUnknown when the output does not list that
// snapshot.
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

func checkYBExportConfig(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	if cfg.BackupS3Endpoint == "" || cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" {
		err := errYBBackupSettingsMissing
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", err.Error()))

		return err
	}

	if cfg.MigratorPassword == "" {
		err := errYBMigratorPasswordMissing
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", err.Error()))

		return err
	}

	if err := requireBackupYBImage(
		ctx,
		cfg,
		"yb-snapshot-export",
		"backup.yb_snapshot.failed",
	); err != nil {
		return err
	}

	return nil
}

func logKeptYBSnapshot(
	ctx context.Context,
	logger *slog.Logger,
	runID string,
	snapshotID string,
	reason string,
) {
	logger.InfoContext(
		ctx,
		"backup.yb_snapshot.snapshot_kept",
		slog.String("run_id", runID),
		slog.String("snapshot_id", snapshotID),
		slog.String("reason", reason),
	)
}

func logDeletedYBSnapshot(
	ctx context.Context,
	logger *slog.Logger,
	runID string,
	snapshotID string,
) {
	logger.InfoContext(
		ctx,
		"backup.yb_snapshot.prior_snapshot_deleted",
		slog.String("run_id", runID),
		slog.String("snapshot_id", snapshotID),
	)
}
