// YugabyteDB snapshot export stores SQL backups outside the data hosts.
// Quorum replication and standby clusters protect against host failure;
// in-cluster snapshots protect against corruption but cannot survive loss of
// their storage. Object-store exports preserve an independent recovery copy.
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
	// ybSnapshotCompleteTimeout limits snapshot creation, including busy nodes.
	ybSnapshotCompleteTimeout = 5 * time.Minute
	// ybSnapshotPollInterval sets the delay between list_snapshots polls.
	ybSnapshotPollInterval = 3 * time.Second
)

// ybSnapshotStatus records the lifecycle state from yb-admin.
type ybSnapshotStatus string

const (
	ybSnapStatusUnknown  ybSnapshotStatus = ""
	ybSnapStatusCreating ybSnapshotStatus = "CREATING"
	ybSnapStatusComplete ybSnapshotStatus = "COMPLETE"
	ybSnapStatusFailed   ybSnapshotStatus = "FAILED"
)

// RunBackupYBSnapshotExport creates a snapshot, waits for COMPLETE, and uploads
// metadata, schema, roles, and a completeness manifest under one run key.
// Helpers use containers against the configured masters without requiring a
// local yugabyte container. Each data guest's `ops backup yb-archive-node`
// timer uploads its tablet archive to the assigned node prefix. Restore
// requires every listed node's complete artifacts.
func RunBackupYBSnapshotExport(ctx context.Context, cfg *config.Config) error {
	logger := telemetry.L(ctx)
	if cfg.BackupS3Endpoint == "" || cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" {
		err := fmt.Errorf(
			"yb-snapshot-export: TACK_BACKUP_S3_ENDPOINT, _ACCESS_KEY_ID, and _SECRET_ACCESS_KEY are required",
		)
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", err.Error()))

		return err
	}

	if cfg.MigratorPassword == "" {
		err := fmt.Errorf("yb-snapshot-export: TACK_MIGRATOR_PASSWORD is required")
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

	filter := "ysql." + cfg.YugabyteDB
	runID := opsNow().UTC().Format(ybSnapshotRunIDLayout)
	stageDir := filepath.Join(cfg.BackupRoot, "yb-snapshot-"+runID)
	if err := os.MkdirAll(stageDir, 0o750); err != nil {
		wrapped := fmt.Errorf("mkdir yb snapshot stage %s: %w", stageDir, err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))

		return wrapped
	}

	// The export container user needs write access to the staging bind mount.
	// Successful uploads permit removing this temporary directory.
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

	// Prior snapshots remain necessary until every node archives its files.
	// Cleanup failures must not prevent a new export.
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

	// Data-guest timers still need the snapshot's files after this command.
	// A later export deletes the snapshot after archive completeness checks.

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

// exportYBSnapshotToObjectStore exports a COMPLETE snapshot's metadata, schema,
// roles, and manifest. The live tablet-server list defines required node
// prefixes; each data guest uploads its own tablet archive separately.
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

	// Uploading the manifest last prevents readers from accepting a run
	// before its required root artifacts exist.
	return uploadYBSnapshotArtifacts(ctx, cfg, ybSnapshotKeyPrefix(runID), artifacts)
}

// listYBTabletServerNodes reads live node names for the manifest rather than
// trusting a configuration list. An empty list cannot establish completeness.
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
			"yb-snapshot-export: list_all_tablet_servers did not report tablet servers: %q",
			strings.TrimSpace(res.Stdout),
		)
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))

		return nil, wrapped
	}

	logger.InfoContext(ctx, "backup.yb_snapshot.tablet_servers", slog.Any("nodes", nodes))

	return nodes, nil
}

// ybSnapshotCleanupMaxRuns limits recent export prefixes examined per cleanup.
// Archivers select only the newest manifest. Older incomplete runs eventually
// require orphan reconciliation instead of indefinite tracking.
const ybSnapshotCleanupMaxRuns = 30

// cleanupYBPriorExportSnapshots deletes prior snapshots only after their node
// archives complete, retaining snapshots that lagging archive timers need.
// Orphan reconciliation excludes schedule-owned snapshots and requires a
// trustworthy manifest reference set. Empty run lists and unresolved manifests
// disable orphan deletion; absent manifests count as resolved. Cleanup logs
// failures and retained-snapshot reasons without blocking the new export.
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

// cleanupYBExportRuns checks bounded prior runs and builds snapshot references.
// Missing prefixes or unresolved manifest reads prevent trustworthy orphan
// identification and return orphanPassOK=false with the reason in the log.
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
			slog.String("reason", "export run prefixes are absent; a reference set cannot be built"),
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
					" did not resolve; the reference set is incomplete",
			),
		)

		return referenced, false
	}

	return referenced, true
}

// cleanupYBExportRun records the manifest's snapshot reference and deletes its
// in-cluster snapshot only after every node artifact exists. It logs retention
// reasons. NotFound counts as a resolved absent manifest; other fetch or parse
// failures omit a required reference and must disable orphan reconciliation.
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
		// An earlier cleanup already deleted this snapshot.
		return true
	}

	if len(manifest.Nodes) == 0 {
		logger.InfoContext(
			ctx,
			"backup.yb_snapshot.snapshot_kept",
			slog.String("run_id", runID),
			slog.String("snapshot_id", manifest.SnapshotID),
			slog.String("reason", "manifest does not list nodes"),
		)

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
		logger.InfoContext(
			ctx,
			"backup.yb_snapshot.snapshot_kept",
			slog.String("run_id", runID),
			slog.String("snapshot_id", manifest.SnapshotID),
			slog.String("reason", "nodes "+strings.Join(missing, ", ")+" have not archived"),
		)

		return true
	}

	if !ybSnapshotDeletable(state) {
		logger.InfoContext(
			ctx,
			"backup.yb_snapshot.snapshot_kept",
			slog.String("run_id", runID),
			slog.String("snapshot_id", manifest.SnapshotID),
			slog.String("reason", "state "+string(state)+" is not deletable"),
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

	logger.InfoContext(
		ctx,
		"backup.yb_snapshot.prior_snapshot_deleted",
		slog.String("run_id", runID),
		slog.String("snapshot_id", manifest.SnapshotID),
	)

	return true
}

// cleanupYBOrphanSnapshots deletes snapshots without references from examined
// export manifests. Snapshot schedules retain ownership of their snapshots.
// Failed schedule listing disables deletion because it cannot distinguish
// schedule snapshots from export orphans.
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

// ybOrphanSnapshotDisposition records deletion eligibility for an unreferenced
// cluster snapshot.
type ybOrphanSnapshotDisposition struct {
	id     string
	delete bool
	reason string
}

// reconcileYBOrphanSnapshots retains scheduled snapshots and non-deletable
// states, and selects other unreferenced snapshots for deletion. ID sorting
// makes the diagnostic order deterministic.
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

// ybSnapshotDeletable permits COMPLETE and FAILED snapshots. CREATING waits
// for later cleanup; DELETING and DELETED already have a deletion in progress.
func ybSnapshotDeletable(state ybSnapshotStatus) bool {
	return state == ybSnapStatusComplete || state == ybSnapStatusFailed
}

// listYBClusterSnapshots returns snapshot IDs and states from list_snapshots.
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

// parseYBClusterSnapshots reads snapshot UUIDs and states. It excludes the
// subsequent restoration section, which also uses UUIDs but not snapshot IDs.
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

// listYBScheduleSnapshotIDs reads PITR-owned IDs from list_snapshot_schedules.
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

// unmarshalYBScheduleSnapshotIDs decodes scheduled snapshot IDs from
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

// ybAdminClusterAccess supplies master addresses and TLS certificate flags
// with read-only directory binds (TACK-460). yb-admin uses the cluster protocol,
// not SQL connection settings. TLS without a certificate directory returns
// only available flags; the command must report its connection failure.
func ybAdminClusterAccess(cfg *config.Config) (args []string, binds []string) {
	args = []string{"--master_addresses", cfg.BackupYBMasterAddresses}
	certsDir := ledgerCertsDir(cfg)
	if !cfg.LedgerTLSEnabled || certsDir == "" {
		return args, nil
	}

	args = append(args, "--certs_dir_name", certsDir)

	return args, []string{certsDir + ":" + certsDir + ":ro"}
}

// ledgerCertsDir trims whitespace and treats blank values as absent paths.
func ledgerCertsDir(cfg *config.Config) string {
	return strings.TrimSpace(cfg.LedgerCertsDir)
}

// ybAdminOneShot runs yb-admin on the Compose network with cluster-access
// flags and optional binds. Staging binds permit export_snapshot output.
// Nonzero exit codes return errors for callers.
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

// parseSnapshotID extracts the UUID from "Started snapshot creation: <uuid>".
// Missing markers or fields return an empty ID for the caller to reject.
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

// waitYBSnapshotComplete polls until COMPLETE or FAILED. A context deadline
// bounds the wait without direct wall-clock checks in the loop.
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
				// The snapshot still requires another poll.
			}
		}

		select {
		case <-pollCtx.Done():
			wrapped := fmt.Errorf(
				"yb-snapshot-export: snapshot %s did not report COMPLETE within %s: %w",
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

// ybSnapshotState reads a matching snapshot's state or returns unknown when
// list_snapshots does not include its ID yet.
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
