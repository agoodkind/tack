package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

const (
	auditPartRestoreTool     = "ops.backfill.once-restore-manifest-part"
	auditPartStatusPlanned   = "planned"
	auditPartStatusRestored  = "restored"
	auditPartStatusPresent   = "already_present"
	auditPartPlannedMissing  = "part_missing"
	auditPartPlannedPresent  = "part_present"
	auditPartOperatorDSNName = "AUDIT_OPERATOR_DSN"
	auditPartReaderDSNName   = "AUDIT_READER_DSN"
)

type auditPartRestoreRequest struct {
	manifest   AuditPartRestoreManifest
	fileDigest string
}

type auditPartRestoreLoaded struct {
	request auditPartRestoreRequest
	reader  *audit.Reader
	state   auditPartRestoreState
}

type auditPartRestoreResult struct {
	clispec.ResultMarker
	auditPartRestoreIdentity
	Command            string `json:"command"`
	Status             string `json:"status"`
	Plan               string `json:"plan,omitempty"`
	ManifestFileSHA256 string `json:"manifest_file_sha256"`
}

func (r auditPartRestoreRequest) result(status, plan string) auditPartRestoreResult {
	return auditPartRestoreResult{
		ResultMarker: clispec.ResultMarker{}, auditPartRestoreIdentity: r.manifest.identity(),
		Command: auditPartRestoreTool, Status: status, Plan: plan, ManifestFileSHA256: r.fileDigest,
	}
}

func loadAuditPartRestore(ctx context.Context, f *cli.Factory, request auditPartRestoreRequest) (auditPartRestoreLoaded, error) {
	reader, err := audit.NewReader(ctx, f.Cfg.AuditReaderDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit_part_restore.reader_failed", slog.String("err", "the ledger reader did not open"))
		return auditPartRestoreLoaded{}, fmt.Errorf("%s: the ledger reader did not open using %s", auditPartRestoreTool, auditPartReaderDSNName)
	}
	state, err := inspectAuditPartRestore(ctx, reader, request.manifest)
	if err != nil {
		reader.Close()
		return auditPartRestoreLoaded{}, err
	}
	return auditPartRestoreLoaded{request: request, reader: reader, state: state}, nil
}

func previewAuditPartRestore(ctx context.Context, f *cli.Factory, request auditPartRestoreRequest, sink clispec.ResultSink) error {
	loaded, err := loadAuditPartRestore(ctx, f, request)
	if err != nil {
		return err
	}
	defer loaded.reader.Close()
	plan := auditPartPlannedMissing
	if loaded.state.Present {
		plan = auditPartPlannedPresent
	}
	return writeAuditPartResult(ctx, sink, request.result(auditPartStatusPlanned, plan))
}

func runAuditPartRestore(ctx context.Context, f *cli.Factory, request auditPartRestoreRequest, sink clispec.ResultSink, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
	loaded, err := loadAuditPartRestore(ctx, f, request)
	if err != nil {
		return outcomeFor(err), err
	}
	defer loaded.reader.Close()
	if loaded.state.Present {
		err := writeAuditPartResult(ctx, sink, request.result(auditPartStatusPresent, ""))
		return outcomeFor(err), err
	}
	details, err := restoreAuditPart(ctx, f, loaded, steps)
	if err != nil {
		return details, err
	}
	err = writeAuditPartResult(ctx, sink, request.result(auditPartStatusRestored, ""))
	return outcomeFor(err), err
}

func restoreAuditPart(ctx context.Context, f *cli.Factory, loaded auditPartRestoreLoaded, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
	m := loaded.request.manifest
	encoded, err := BuildAuditPartChange(ctx, m, loaded.request.fileDigest, loaded.state.Payload)
	if err != nil {
		return outcomeFor(err), err
	}
	if f.Cfg.AuditOperatorDSN == "" {
		err := fmt.Errorf("%s: %s is required", auditPartRestoreTool, auditPartOperatorDSNName)
		return outcomeFor(err), err
	}
	pool, err := pgxpool.New(ctx, f.Cfg.AuditOperatorDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit_part_restore.operator_pool_failed", slog.String("err", "the operator pool did not open"))
		wrapped := fmt.Errorf("%s: the operator pool did not open using %s", auditPartRestoreTool, auditPartOperatorDSNName)
		return outcomeFor(wrapped), wrapped
	}
	defer pool.Close()
	planned, err := encodeEvidence(ctx, m.identity())
	if err != nil {
		return outcomeFor(err), err
	}
	return recordedStep(ctx, steps, audit.ExternalStep(auditPartTarget(m.TargetEventID, m.PartIndex), planned),
		func(ctx context.Context) error { return callAuditPartRestore(ctx, pool, m, encoded) },
		func(ctx context.Context) (json.RawMessage, error) {
			observed, err := observeRestoredAuditPart(ctx, loaded.reader, m, loaded.state)
			if err != nil {
				return nil, err
			}
			return encodeEvidence(ctx, observed)
		})
}

type auditPartRestoreObserved struct {
	auditPartRestoreIdentity
	PartDigest    string `json:"part_digest"`
	PayloadBytes  int    `json:"payload_bytes"`
	TargetSeq     int64  `json:"target_seq"`
	TargetShard   int16  `json:"target_shard"`
	TargetRowHash []byte `json:"target_row_hash"`
}

func (auditPartRestoreObserved) isStepEvidence() {}

func observeRestoredAuditPart(ctx context.Context, reader *audit.Reader, m AuditPartRestoreManifest, before auditPartRestoreState) (auditPartRestoreObserved, error) {
	verification, err := reader.VerifyManifest(ctx, m.OrgID, m.TargetEventID)
	if err != nil {
		return auditPartRestoreObserved{}, partRestoreFailure(ctx, "cannot verify the restored event "+m.TargetEventID.String(), err)
	}
	if err := checkVerification(m, verification); err != nil {
		return auditPartRestoreObserved{}, partRestoreFailure(ctx, "cannot check the restored event "+m.TargetEventID.String(), err)
	}
	parts, err := reader.ManifestParts(ctx, m.OrgID, m.TargetEventID)
	if err != nil || len(parts) != 1 {
		return auditPartRestoreObserved{}, partRestoreFailure(ctx, "cannot read the restored part of event "+m.TargetEventID.String(), errors.Join(err, errAuditPartRestoreRefused))
	}
	after, err := reader.GetByID(ctx, m.TargetEventID)
	if err != nil {
		return auditPartRestoreObserved{}, partRestoreFailure(ctx, "cannot read the restored event "+m.TargetEventID.String(), err)
	}
	if !sameLedgerRow(before.Target, *after) {
		return auditPartRestoreObserved{}, partRestoreFailure(ctx, "the ledger row differs for event "+m.TargetEventID.String(), errAuditPartRestoreRefused)
	}
	return auditPartRestoreObserved{
		auditPartRestoreIdentity: m.identity(), PartDigest: parts[0].Digest, PayloadBytes: len(parts[0].Payload),
		TargetSeq: after.Seq, TargetShard: after.Shard, TargetRowHash: after.RowHash,
	}, nil
}

func writeAuditPartResult(ctx context.Context, sink clispec.ResultSink, result auditPartRestoreResult) error {
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		return partRestoreFailure(ctx, "cannot write the part restore result", err)
	}
	return nil
}
