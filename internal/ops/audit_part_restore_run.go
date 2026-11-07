package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
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
	auditPartRefusedCode     = "restore_refused"
	auditPartPlannedMissing  = "part_missing"
	auditPartPlannedPresent  = "part_present"
	auditPartOperatorDSNName = "AUDIT_OPERATOR_DSN"
	auditPartReaderDSNName   = "AUDIT_READER_DSN"
)

type auditPartRestoreResult struct {
	clispec.ResultMarker
	Command            string    `json:"command"`
	Status             string    `json:"status"`
	Plan               string    `json:"plan,omitempty"`
	OrgID              uuid.UUID `json:"org_id"`
	TargetEventID      uuid.UUID `json:"target_event_id"`
	SourceEventID      uuid.UUID `json:"source_event_id"`
	PartIndex          int64     `json:"part_index"`
	RootDigest         string    `json:"root_digest"`
	ManifestFileSHA256 string    `json:"manifest_file_sha256"`
}

type auditPartRestoreEvidence struct {
	OrgID         uuid.UUID `json:"org_id"`
	TargetEventID uuid.UUID `json:"target_event_id"`
	SourceEventID uuid.UUID `json:"source_event_id"`
	PartIndex     int64     `json:"part_index"`
	RootDigest    string    `json:"root_digest"`
	RowsInserted  int64     `json:"rows_inserted,omitempty"`
}

func (auditPartRestoreEvidence) isStepEvidence() {}

func newAuditPartRestoreResult(m AuditPartRestoreManifest, fileDigest, status string) auditPartRestoreResult {
	return auditPartRestoreResult{
		ResultMarker: clispec.ResultMarker{}, Command: auditPartRestoreTool, Status: status, Plan: "",
		OrgID: m.OrgID, TargetEventID: m.TargetEventID, SourceEventID: m.SourceEventID, PartIndex: m.PartIndex,
		RootDigest: m.ManifestSHA256, ManifestFileSHA256: fileDigest,
	}
}

func openAuditPartRestoreReader(ctx context.Context, f *cli.Factory) (*audit.Reader, error) {
	reader, err := audit.NewReader(ctx, f.Cfg.AuditReaderDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit_part_restore.reader_failed", slog.String("err", "the ledger reader did not open"))
		return nil, fmt.Errorf("%s: open the ledger reader: check %s", auditPartRestoreTool, auditPartReaderDSNName)
	}
	return reader, nil
}

func previewAuditPartRestore(ctx context.Context, f *cli.Factory, in auditPartRestoreFlags, sink clispec.ResultSink) error {
	manifest, fileDigest, err := readAuditPartRestoreManifest(ctx, f.Cfg.RepairDir, in.Manifest, in.ManifestSHA256)
	if err != nil {
		return err
	}
	reader, err := openAuditPartRestoreReader(ctx, f)
	if err != nil {
		return err
	}
	defer reader.Close()
	state, err := inspectAuditPartRestore(ctx, reader, manifest)
	if err != nil {
		return err
	}
	result := newAuditPartRestoreResult(manifest, fileDigest, auditPartStatusPlanned)
	result.Plan = auditPartPlannedMissing
	if state.Present {
		result.Plan = auditPartPlannedPresent
	}
	return writeAuditPartResult(ctx, sink, result)
}

func runAuditPartRestore(ctx context.Context, f *cli.Factory, in auditPartRestoreFlags, sink clispec.ResultSink, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
	manifest, fileDigest, err := readAuditPartRestoreManifest(ctx, f.Cfg.RepairDir, in.Manifest, in.ManifestSHA256)
	if err != nil {
		return commandOutcome(audit.OutcomeRefused, invalidInputCode, err), err
	}
	reader, err := openAuditPartRestoreReader(ctx, f)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	defer reader.Close()
	state, err := inspectAuditPartRestore(ctx, reader, manifest)
	if errors.Is(err, errAuditPartRestoreRefused) {
		return commandOutcome(audit.OutcomeRefused, auditPartRefusedCode, err), err
	}
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	if state.Present {
		result := newAuditPartRestoreResult(manifest, fileDigest, auditPartStatusPresent)
		return commandOutcome(audit.OutcomeCompleted, "", nil), writeAuditPartResult(ctx, sink, result)
	}
	details, err := restoreAuditPart(ctx, f, reader, manifest, fileDigest, state, steps)
	if err != nil {
		return details, err
	}
	result := newAuditPartRestoreResult(manifest, fileDigest, auditPartStatusRestored)
	return details, writeAuditPartResult(ctx, sink, result)
}

func writeAuditPartResult(ctx context.Context, sink clispec.ResultSink, result auditPartRestoreResult) error {
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		return partRestoreFailure(ctx, "write the part restore result", err)
	}
	return nil
}

func restoreAuditPart(
	ctx context.Context, f *cli.Factory, reader *audit.Reader, m AuditPartRestoreManifest, fileDigest string,
	state auditPartRestoreState, steps audit.StepRecorder,
) (audit.OutcomeDetails, error) {
	payload, err := readSourcePartPayload(ctx, reader, m)
	if errors.Is(err, errAuditPartRestoreRefused) {
		return commandOutcome(audit.OutcomeRefused, auditPartRefusedCode, err), err
	}
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	encoded, err := buildAuditPartChange(ctx, m, fileDigest, payload)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	if f.Cfg.AuditOperatorDSN == "" {
		err := fmt.Errorf("%s: %s is required", auditPartRestoreTool, auditPartOperatorDSNName)
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	pool, err := pgxpool.New(ctx, f.Cfg.AuditOperatorDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit_part_restore.operator_pool_failed", slog.String("err", "the operator pool did not open"))
		wrapped := fmt.Errorf("%s: open the operator pool: check %s", auditPartRestoreTool, auditPartOperatorDSNName)
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, wrapped), wrapped
	}
	defer pool.Close()
	evidence := auditPartRestoreEvidence{
		OrgID: m.OrgID, TargetEventID: m.TargetEventID, SourceEventID: m.SourceEventID, PartIndex: m.PartIndex,
		RootDigest: m.ManifestSHA256, RowsInserted: 0,
	}
	planned, err := encodeEvidence(ctx, evidence)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	return recordedStep(ctx, steps, audit.ExternalStep(auditPartTarget(m.TargetEventID, m.PartIndex), planned),
		func(ctx context.Context) error {
			inserted, err := callAuditPartRestore(ctx, pool, m, encoded)
			evidence.RowsInserted = inserted
			return err
		},
		func(ctx context.Context) (json.RawMessage, error) {
			if err := verifyRestoredAuditPart(ctx, reader, m, state); err != nil {
				return nil, err
			}
			return encodeEvidence(ctx, evidence)
		})
}

func verifyRestoredAuditPart(ctx context.Context, reader *audit.Reader, m AuditPartRestoreManifest, before auditPartRestoreState) error {
	verification, err := reader.VerifyManifest(ctx, m.OrgID, m.TargetEventID)
	if err != nil {
		return partRestoreFailure(ctx, "verify the restored event "+m.TargetEventID.String(), err)
	}
	if err := checkVerification(m, verification); err != nil {
		return partRestoreFailure(ctx, "check the restored event "+m.TargetEventID.String(), err)
	}
	after, err := reader.GetByID(ctx, m.TargetEventID)
	if err != nil {
		return partRestoreFailure(ctx, "read the restored event "+m.TargetEventID.String(), err)
	}
	if !sameLedgerRow(before.Target, *after) {
		return partRestoreFailure(ctx, "compare the ledger row of event "+m.TargetEventID.String(), errAuditPartRestoreRefused)
	}
	return nil
}
