package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

const (
	// exportIncompleteCode is the error code of a snapshot export that
	// uploaded run objects and then failed.
	exportIncompleteCode = "export_run_incomplete"
	// drillLegSkippedCode is the error code of a restore drill that skipped
	// the FoundationDB leg.
	drillLegSkippedCode = "drill_leg_skipped"
	// drillYugabyteLeg is the committed step of a drill that ran only the
	// YugabyteDB leg.
	drillYugabyteLeg = "restore-drill/yugabyte"
)

type snapshotExportEvidence struct {
	Bucket      string   `json:"bucket"`
	Filter      string   `json:"filter"`
	NewRunIDs   []string `json:"new_run_ids,omitempty"`
	ManifestKey string   `json:"manifest_key,omitempty"`
	SnapshotID  string   `json:"snapshot_id,omitempty"`
	Nodes       []string `json:"nodes,omitempty"`
	Artifacts   []string `json:"artifacts,omitempty"`
}

func (snapshotExportEvidence) isStepEvidence() {}

type restoreDrillEvidence struct {
	YBRunKey      string `json:"yb_run_key,omitempty"`
	FDBTargetTime string `json:"fdb_target_time,omitempty"`
	FDBLeg        string `json:"fdb_leg"`
}

func (restoreDrillEvidence) isStepEvidence() {}

// runBackupYBSnapshotExportRecorded runs yb-snapshot-export as one step. It
// lists the export runs before and after the export and records the manifest
// key, snapshot ID, nodes, and artifacts of the new run. A failed export that
// left a new run prefix returns partially_completed with that prefix.
func runBackupYBSnapshotExportRecorded(ctx context.Context, cfg *config.Config, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
	if cfg.BackupS3Endpoint == "" || cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" ||
		cfg.MigratorPassword == "" || cfg.BackupYBImage == "" {
		err := RunBackupYBSnapshotExport(ctx, cfg)
		return commandOutcome(audit.OutcomeRefused, commandConfigCode, err), err
	}
	client := newBackupS3Client(cfg)
	bucket := cfg.BackupS3BucketMain
	before, err := listYBSnapshotRunIDs(ctx, client, bucket)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	planned := snapshotExportEvidence{
		Bucket: bucket, Filter: "ysql." + cfg.YugabyteDB, NewRunIDs: nil, ManifestKey: "", SnapshotID: "", Nodes: nil, Artifacts: nil,
	}
	plannedJSON, err := encodeEvidence(ctx, planned)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	step := audit.ExternalStep(externalTarget("s3/"+bucket+"/"+ybSnapshotRootPrefix), plannedJSON)
	details, runErr := recordedStep(ctx, steps, step,
		func(ctx context.Context) error { return RunBackupYBSnapshotExport(ctx, cfg) },
		func(ctx context.Context) (json.RawMessage, error) {
			return observeSnapshotExport(ctx, client, planned, before)
		})
	if runErr == nil {
		return details, nil
	}
	created, listErr := newSnapshotRuns(ctx, client, bucket, before)
	if listErr != nil {
		slog.ErrorContext(ctx, "backup.yb_snapshot.observe_failed", slog.String("err", listErr.Error()))
		return details, errors.Join(runErr, listErr)
	}
	if len(created) == 0 {
		return details, runErr
	}
	partial := commandOutcome(audit.OutcomePartiallyCompleted, exportIncompleteCode, runErr)
	for _, runID := range created {
		partial.CommittedSteps = append(partial.CommittedSteps, ybSnapshotKeyPrefix(runID))
	}
	return partial, runErr
}

func newSnapshotRuns(ctx context.Context, client *s3.Client, bucket string, before []string) ([]string, error) {
	after, err := listYBSnapshotRunIDs(ctx, client, bucket)
	if err != nil {
		return nil, err
	}
	var created []string
	for _, runID := range after {
		if !slices.Contains(before, runID) {
			created = append(created, runID)
		}
	}
	return created, nil
}

func observeSnapshotExport(ctx context.Context, client *s3.Client, planned snapshotExportEvidence, before []string) (json.RawMessage, error) {
	created, err := newSnapshotRuns(ctx, client, planned.Bucket, before)
	if err != nil {
		return nil, err
	}
	if len(created) != 1 {
		err := fmt.Errorf("yb-snapshot-export: bucket %s has %d new runs instead of one", planned.Bucket, len(created))
		slog.ErrorContext(ctx, "backup.yb_snapshot.observe_failed", slog.String("err", err.Error()))
		return nil, err
	}
	manifest, err := fetchYBSnapshotManifest(ctx, client, planned.Bucket, created[0])
	if err != nil {
		return nil, err
	}
	observed := planned
	observed.NewRunIDs = created
	observed.ManifestKey = ybSnapshotKeyPrefix(created[0]) + ybSnapshotManifestObject
	observed.SnapshotID = manifest.SnapshotID
	observed.Artifacts = manifest.Artifacts
	for _, node := range manifest.Nodes {
		observed.Nodes = append(observed.Nodes, node.Name)
	}
	return encodeEvidence(ctx, observed)
}

// runBackupRestoreDrillRecorded runs restore-drill as one step. A drill that
// passes without the FoundationDB leg returns partially_completed with the
// YugabyteDB leg as its committed step.
func runBackupRestoreDrillRecorded(ctx context.Context, cfg *config.Config, opts RestoreDrillOptions, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
	if cfg.BackupS3Endpoint == "" || cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" {
		err := RunBackupRestoreDrill(ctx, cfg, opts)
		return commandOutcome(audit.OutcomeRefused, commandConfigCode, err), err
	}
	planned := restoreDrillEvidence{YBRunKey: opts.YBRunKey, FDBTargetTime: "", FDBLeg: "run"}
	if opts.FDBTargetTime != nil {
		planned.FDBTargetTime = opts.FDBTargetTime.UTC().Format("2006-01-02T15:04:05Z")
	}
	if !cfg.BackupFDBContinuous {
		planned.FDBLeg = "skipped"
	}
	evidence, err := encodeEvidence(ctx, planned)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	step := audit.ExternalStep(externalTarget("backup/restore-drill/"+cfg.BackupS3BucketMain), evidence)
	details, runErr := recordedStep(ctx, steps, step,
		func(ctx context.Context) error { return RunBackupRestoreDrill(ctx, cfg, opts) },
		func(context.Context) (json.RawMessage, error) { return evidence, nil })
	if runErr != nil || cfg.BackupFDBContinuous {
		return details, runErr
	}
	skipped := commandOutcome(audit.OutcomePartiallyCompleted, drillLegSkippedCode,
		errors.New("the drill skipped the FoundationDB leg because TACK_BACKUP_FDB_CONTINUOUS is false"))
	skipped.CommittedSteps = []string{drillYugabyteLeg}
	return skipped, nil
}
