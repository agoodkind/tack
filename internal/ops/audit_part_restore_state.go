package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

const auditPartRefusedCode = "restore_refused"

var errAuditPartRestoreRefused = errors.New("the part restore preconditions do not hold")

type auditPartRestoreIdentity struct {
	OrgID         uuid.UUID `json:"org_id"`
	TargetEventID uuid.UUID `json:"target_event_id"`
	SourceEventID uuid.UUID `json:"source_event_id"`
	PartIndex     int64     `json:"part_index"`
	RootDigest    string    `json:"root_digest"`
}

func (auditPartRestoreIdentity) isStepEvidence() {}

func (manifest AuditPartRestoreManifest) identity() auditPartRestoreIdentity {
	return auditPartRestoreIdentity{
		OrgID: manifest.OrgID, TargetEventID: manifest.TargetEventID, SourceEventID: manifest.SourceEventID,
		PartIndex: manifest.PartIndex, RootDigest: manifest.ManifestSHA256,
	}
}

type auditPartRestoreState struct {
	Target  audit.Row
	Present bool
	Payload []byte
}

func outcomeFor(err error) audit.OutcomeDetails {
	switch {
	case err == nil:
		return commandOutcome(audit.OutcomeCompleted, "", nil)
	case errors.Is(err, errAuditPartRestoreManifest):
		return commandOutcome(audit.OutcomeRefused, invalidInputCode, err)
	case errors.Is(err, errAuditPartRestoreRefused):
		return commandOutcome(audit.OutcomeRefused, auditPartRefusedCode, err)
	default:
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err)
	}
}

type auditPartRestoreOutcomeError struct {
	details audit.OutcomeDetails
	cause   error
}

func (e auditPartRestoreOutcomeError) Error() string { return e.cause.Error() }

func (e auditPartRestoreOutcomeError) Unwrap() error { return e.cause }

func (e auditPartRestoreOutcomeError) ReadOutcome() audit.OutcomeDetails { return e.details }

func previewFailure(err error) error {
	if !errors.Is(err, errAuditPartRestoreManifest) && !errors.Is(err, errAuditPartRestoreRefused) {
		return err
	}
	return auditPartRestoreOutcomeError{details: outcomeFor(err), cause: err}
}

func refusePartRestore(ctx context.Context, reason string) error {
	return refuseWith(ctx, errAuditPartRestoreRefused, reason)
}

func refuseWith(ctx context.Context, sentinel error, reason string) error {
	err := fmt.Errorf("%w: %s", sentinel, reason)
	slog.ErrorContext(ctx, "audit_part_restore.refused", slog.String("err", err.Error()))
	return err
}

func partRestoreFailure(ctx context.Context, step string, err error) error {
	wrapped := fmt.Errorf("%s: %w", step, err)
	slog.ErrorContext(ctx, "audit_part_restore.step_failed", slog.String("err", wrapped.Error()))
	return wrapped
}

func inspectAuditPartRestore(
	ctx context.Context,
	reader *audit.Reader,
	manifest AuditPartRestoreManifest,
) (auditPartRestoreState, error) {
	source, err := reader.VerifyManifest(ctx, manifest.OrgID, manifest.SourceEventID)
	if err != nil {
		return auditPartRestoreState{}, refusePartRestore(ctx,
			fmt.Sprintf("verify the source event %s: %v", manifest.SourceEventID, err))
	}
	err = checkVerification(manifest, source)
	if err != nil {
		return auditPartRestoreState{}, refusePartRestore(ctx,
			fmt.Sprintf("source event %s: %v", manifest.SourceEventID, err))
	}
	target, err := readPinnedRow(ctx, reader, manifest,
		manifest.TargetEventID, manifest.TargetSeq, manifest.TargetShard, manifest.TargetRowHash)
	if err != nil {
		return auditPartRestoreState{}, err
	}

	if target.HashVersion != manifest.TargetHashVersion {
		return auditPartRestoreState{}, refusePartRestore(ctx,
			fmt.Sprintf("target hash version %d is not %d", target.HashVersion, manifest.TargetHashVersion))
	}
	_, err = readPinnedRow(ctx, reader, manifest,
		manifest.SourceEventID, manifest.SourceSeq, manifest.SourceShard, manifest.SourceRowHash)
	if err != nil {
		return auditPartRestoreState{}, err
	}

	digests, err := reader.ManifestPartDigests(ctx, manifest.OrgID, manifest.TargetEventID)
	if err != nil {
		return auditPartRestoreState{}, partRestoreFailure(ctx,
			"cannot read the part digests of event "+manifest.TargetEventID.String(), err)
	}
	if len(digests) == 0 {
		return missingPartState(ctx, reader, manifest, target)
	}
	if len(digests) != 1 || digests[0].Index != 0 || digests[0].Digest != manifest.ManifestSHA256 {
		return auditPartRestoreState{}, refusePartRestore(ctx,
			fmt.Sprintf("event %s stores %d parts that differ from the frozen part", manifest.TargetEventID, len(digests)))
	}

	return auditPartRestoreState{Target: target, Present: true, Payload: nil}, nil
}

func missingPartState(
	ctx context.Context,
	reader *audit.Reader,
	manifest AuditPartRestoreManifest,
	target audit.Row,
) (auditPartRestoreState, error) {
	parts, err := reader.ManifestParts(ctx, manifest.OrgID, manifest.SourceEventID)
	if err != nil {
		return auditPartRestoreState{}, partRestoreFailure(ctx,
			"cannot read the source part of event "+manifest.SourceEventID.String(), err)
	}
	if len(parts) != 1 || parts[0].Index != 0 {
		return auditPartRestoreState{}, refusePartRestore(ctx,
			fmt.Sprintf("source event %s stores %d parts, want part 0 only", manifest.SourceEventID, len(parts)))
	}
	return auditPartRestoreState{Target: target, Present: false, Payload: parts[0].Payload}, nil
}

func checkVerification(manifest AuditPartRestoreManifest, verification audit.ManifestVerification) error {
	if !verification.PayloadVerified || verification.IdentityCount != manifest.IdentityCount ||
		verification.DeclaredParts != manifest.DeclaredParts || verification.PartCount != manifest.DeclaredParts ||
		verification.DeclaredDigest != manifest.ManifestSHA256 || verification.RecomputedDigest != manifest.ManifestSHA256 {
		return fmt.Errorf("verification %+v %w", verification, errAuditPartVerification)
	}
	return nil
}

func readPinnedRow(
	ctx context.Context,
	reader *audit.Reader,
	manifest AuditPartRestoreManifest,
	eventID uuid.UUID,
	seq int64,
	shard int16,
	rowHash []byte,
) (audit.Row, error) {
	row, err := reader.GetByID(ctx, eventID)
	if err != nil {
		return audit.Row{}, refusePartRestore(ctx, fmt.Sprintf("read event %s: %v", eventID, err))
	}
	if row.OrgID != manifest.OrgID || row.Seq != seq || row.Shard != shard || !bytes.Equal(row.RowHash, rowHash) {
		return audit.Row{}, refusePartRestore(ctx,
			fmt.Sprintf("event %s differs from its frozen org, seq, shard, or row hash", eventID))
	}
	return *row, nil
}

func sameLedgerRow(before, after audit.Row) bool {
	return before.EventID == after.EventID && before.Seq == after.Seq && before.Shard == after.Shard &&
		before.HashVersion == after.HashVersion && bytes.Equal(before.PrevHash, after.PrevHash) &&
		bytes.Equal(before.RowHash, after.RowHash)
}
