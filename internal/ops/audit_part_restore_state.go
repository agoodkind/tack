package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

var errAuditPartRestoreRefused = errors.New("the part restore preconditions do not hold")

type auditPartRestoreState struct {
	Target  audit.Row
	Present bool
}

func refusePartRestore(ctx context.Context, reason string) error {
	return refuseWith(ctx, errAuditPartRestoreRefused, reason)
}

func refuseWith(ctx context.Context, sentinel error, reason string) error {
	err := fmt.Errorf("%w: %s", sentinel, reason)
	slog.ErrorContext(ctx, "audit_part_restore.refused", slog.String("err", err.Error()))
	return err
}

func inspectAuditPartRestore(ctx context.Context, reader *audit.Reader, m AuditPartRestoreManifest) (auditPartRestoreState, error) {
	source, err := reader.VerifyManifest(ctx, m.OrgID, m.SourceEventID)
	if err != nil {
		return auditPartRestoreState{}, refusePartRestore(ctx, fmt.Sprintf("verify the source event %s: %v", m.SourceEventID, err))
	}
	if err := checkVerification(m, source); err != nil {
		return auditPartRestoreState{}, refusePartRestore(ctx, fmt.Sprintf("source event %s: %v", m.SourceEventID, err))
	}
	target, err := readPinnedRow(ctx, reader, m, m.TargetEventID, m.TargetSeq, m.TargetShard, m.TargetRowHash)
	if err != nil {
		return auditPartRestoreState{}, err
	}
	if target.HashVersion != m.TargetHashVersion {
		return auditPartRestoreState{}, refusePartRestore(ctx, fmt.Sprintf("target hash version %d is not %d", target.HashVersion, m.TargetHashVersion))
	}
	if _, err := readPinnedRow(ctx, reader, m, m.SourceEventID, m.SourceSeq, m.SourceShard, m.SourceRowHash); err != nil {
		return auditPartRestoreState{}, err
	}
	digests, err := reader.ManifestPartDigests(ctx, m.OrgID, m.TargetEventID)
	if err != nil {
		return auditPartRestoreState{}, partRestoreFailure(ctx, "read the part digests of event "+m.TargetEventID.String(), err)
	}
	if len(digests) == 0 {
		return auditPartRestoreState{Target: target, Present: false}, nil
	}
	if len(digests) != 1 || digests[0].Index != 0 || digests[0].Digest != m.PartDigest {
		return auditPartRestoreState{}, refusePartRestore(ctx, fmt.Sprintf("event %s stores %d parts that differ from the frozen part", m.TargetEventID, len(digests)))
	}
	return auditPartRestoreState{Target: target, Present: true}, nil
}

func readSourcePartPayload(ctx context.Context, reader *audit.Reader, m AuditPartRestoreManifest) ([]byte, error) {
	parts, err := reader.ManifestParts(ctx, m.OrgID, m.SourceEventID)
	if err != nil {
		return nil, partRestoreFailure(ctx, "read the source part of event "+m.SourceEventID.String(), err)
	}
	if len(parts) != 1 || parts[0].Index != 0 {
		return nil, refusePartRestore(ctx, fmt.Sprintf("source event %s stores %d parts, want part 0 only", m.SourceEventID, len(parts)))
	}
	digest := sha256.Sum256(parts[0].Payload)
	if hex.EncodeToString(digest[:]) != m.ManifestSHA256 {
		return nil, refusePartRestore(ctx, fmt.Sprintf("source event %s part 0 does not hash to the frozen root", m.SourceEventID))
	}
	return parts[0].Payload, nil
}

func checkVerification(m AuditPartRestoreManifest, verification audit.ManifestVerification) error {
	if !verification.PayloadVerified || verification.IdentityCount != m.IdentityCount ||
		verification.DeclaredParts != m.DeclaredParts || verification.PartCount != m.DeclaredParts ||
		verification.DeclaredDigest != m.ManifestSHA256 || verification.RecomputedDigest != m.ManifestSHA256 {
		return fmt.Errorf("verification %+v does not match the frozen manifest", verification)
	}
	return nil
}

func readPinnedRow(ctx context.Context, reader *audit.Reader, m AuditPartRestoreManifest, eventID uuid.UUID, seq int64, shard int16, rowHash []byte) (audit.Row, error) {
	row, err := reader.GetByID(ctx, eventID)
	if err != nil {
		return audit.Row{}, refusePartRestore(ctx, fmt.Sprintf("read event %s: %v", eventID, err))
	}
	if row.OrgID != m.OrgID || row.Seq != seq || row.Shard != shard || !bytes.Equal(row.RowHash, rowHash) {
		return audit.Row{}, refusePartRestore(ctx, fmt.Sprintf("event %s differs from its frozen org, seq, shard, or row hash", eventID))
	}
	return *row, nil
}

func sameLedgerRow(before, after audit.Row) bool {
	return before.EventID == after.EventID && before.Seq == after.Seq && before.Shard == after.Shard &&
		before.HashVersion == after.HashVersion && bytes.Equal(before.PrevHash, after.PrevHash) &&
		bytes.Equal(before.RowHash, after.RowHash)
}
