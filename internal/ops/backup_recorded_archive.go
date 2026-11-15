package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

const archiveUploadIncompleteCode = "archive_upload_incomplete"

type nodeArchiveEvidence struct {
	Bucket     string   `json:"bucket"`
	RunID      string   `json:"run_id"`
	SnapshotID string   `json:"snapshot_id"`
	Node       string   `json:"node"`
	KeyPrefix  string   `json:"key_prefix"`
	Artifacts  []string `json:"artifacts"`
}

func (nodeArchiveEvidence) isStepEvidence() {}

func runBackupYBArchiveNodeRecorded(
	ctx context.Context,
	cfg *config.Config,
	runID string,
	steps audit.StepRecorder,
) (audit.OutcomeDetails, error) {
	if err := requireYBArchiveConfig(ctx, cfg); err != nil {
		return commandOutcome(audit.OutcomeRefused, commandConfigCode, err), err
	}

	cli, err := newDockerClient(ctx)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	defer cli.Close()
	nodeName, err := localYBNodeName(ctx, cli)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}

	client := newBackupS3Client(cfg)
	target, err := resolveYBArchiveTarget(ctx, cfg, client, nodeName, runID)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}

	if target == nil {
		return commandOutcome(audit.OutcomeCompleted, "", nil), nil
	}

	planned := nodeArchiveEvidence{
		Bucket:     cfg.BackupS3BucketMain,
		RunID:      target.manifest.RunID,
		SnapshotID: target.manifest.SnapshotID,
		Node:       nodeName,
		KeyPrefix:  target.prefix,
		Artifacts:  archiveArtifactKeys(target.prefix),
	}
	plannedJSON, err := encodeEvidence(ctx, planned)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}

	step := audit.ExternalStep(externalTarget("s3/"+planned.Bucket+"/"+target.prefix), plannedJSON)
	if target.complete {
		return recordedStep(
			ctx,
			steps,
			step,
			func(ctx context.Context) error {
				return verifyNodeArchive(ctx, client, planned)
			},
		)
	}

	details, runErr := recordedStep(
		ctx,
		steps,
		step,
		func(ctx context.Context) error {
			if err := uploadYBNodeArchive(ctx, cfg, cli, nodeName, target); err != nil {
				return err
			}

			return verifyNodeArchive(ctx, client, planned)
		},
	)
	if runErr == nil {
		return details, nil
	}

	present, listErr := presentArchiveArtifacts(ctx, client, planned)
	if listErr != nil {
		slog.ErrorContext(
			ctx,
			"backup.yb_archive.observe_failed",
			slog.String("err", listErr.Error()),
		)

		return details, errors.Join(runErr, listErr)
	}

	if len(present) == 0 {
		return details, runErr
	}

	partial := commandOutcome(audit.OutcomePartiallyCompleted, archiveUploadIncompleteCode, runErr)
	partial.CommittedSteps = present

	return partial, runErr
}

func archiveArtifactKeys(prefix string) []string {
	objects := ybNodeArtifactObjects()

	keys := make([]string, 0, len(objects))
	for _, object := range objects {
		keys = append(keys, prefix+object)
	}

	return keys
}

func presentArchiveArtifacts(
	ctx context.Context,
	client *s3.Client,
	planned nodeArchiveEvidence,
) ([]string, error) {
	var present []string

	for _, key := range planned.Artifacts {
		exists, err := objectExists(ctx, client, planned.Bucket, key)
		if err != nil {
			return nil, err
		}

		if exists {
			present = append(present, key)
		}
	}

	return present, nil
}

// verifyNodeArchive checks that the bucket contains every expected artifact.
func verifyNodeArchive(
	ctx context.Context,
	client *s3.Client,
	planned nodeArchiveEvidence,
) error {
	present, err := presentArchiveArtifacts(ctx, client, planned)
	if err != nil {
		return err
	}

	if len(present) != len(planned.Artifacts) {
		err := fmt.Errorf(
			"yb-archive-node: only %d of %d artifacts under %s exist in bucket %s",
			len(present),
			len(planned.Artifacts),
			planned.KeyPrefix,
			planned.Bucket,
		)
		slog.ErrorContext(ctx, "backup.yb_archive.observe_failed", slog.String("err", err.Error()))

		return err
	}

	return nil
}
