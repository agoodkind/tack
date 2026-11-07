package ops

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

//go:embed audit_part_restore.sql
var auditPartRestoreStatement string

var auditPartRestoreNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("tack:audit-part-restore"))

type auditPartRecord struct {
	OrgID     uuid.UUID                `json:"org_id"`
	EventID   uuid.UUID                `json:"event_id"`
	PartIndex int64                    `json:"part_index"`
	Digest    string                   `json:"digest"`
	Payload   audit.ProtectedReference `json:"payload"`
}

type auditPartProvenance struct {
	SourceEventID      uuid.UUID `json:"source_event_id"`
	ManifestFileSHA256 string    `json:"manifest_file_sha256"`
}

func auditPartTarget(eventID uuid.UUID, partIndex int64) audit.Target {
	uri := auditEventURI(eventID) + auditEventPartURISegment + strconv.FormatInt(partIndex, 10)
	target := auditResourceTarget(uri)
	target.NodeType = audit.TargetAuditEventPart
	target.ID = eventID
	return target
}

func auditPartChangeEventID(manifestFileDigest string, m AuditPartRestoreManifest) uuid.UUID {
	return uuid.NewSHA1(auditPartRestoreNamespace, []byte(manifestFileDigest+"/"+auditPartTarget(m.TargetEventID, m.PartIndex).URI))
}

func buildAuditPartChange(ctx context.Context, m AuditPartRestoreManifest, manifestFileDigest string, payload []byte) (audit.EncodedEvent, error) {
	operation, err := audit.RequireMutationContext(ctx)
	if err != nil {
		return audit.EncodedEvent{}, partRestoreFailure(ctx, "read the operation of the part restore", err)
	}
	operation.Context.OrgID = m.OrgID
	provenance, err := json.Marshal(auditPartProvenance{SourceEventID: m.SourceEventID, ManifestFileSHA256: manifestFileDigest})
	if err != nil {
		return audit.EncodedEvent{}, partRestoreFailure(ctx, "encode the part provenance", err)
	}
	operation.Extra = provenance
	changeID := auditPartChangeEventID(manifestFileDigest, m)
	target := auditPartTarget(m.TargetEventID, m.PartIndex)
	payloadValue, err := json.Marshal(base64.StdEncoding.EncodeToString(payload))
	if err != nil {
		return audit.EncodedEvent{}, partRestoreFailure(ctx, "encode the restored part payload", err)
	}
	payloadRef := uuid.NewSHA1(auditPartRestoreNamespace, []byte(changeID.String()+"/payload"))
	record := auditPartRecord{
		OrgID: m.OrgID, EventID: m.TargetEventID, PartIndex: m.PartIndex, Digest: m.PartDigest,
		Payload: audit.ProtectedReference{PIIRef: payloadRef},
	}
	after, err := json.Marshal(record)
	if err != nil {
		return audit.EncodedEvent{}, partRestoreFailure(ctx, "encode the restored part record", err)
	}
	change, err := audit.NewChange(changeID, target, nil, after)
	if err != nil {
		return audit.EncodedEvent{}, partRestoreFailure(ctx, "build the part change", err)
	}
	change.Protected = []audit.ProtectedValue{{Ref: payloadRef, SubjectID: target.ID, Value: payloadValue}}
	event, err := audit.ChangeEvent(operation, change, clock.Now())
	if err != nil {
		return audit.EncodedEvent{}, partRestoreFailure(ctx, "build the part change event", err)
	}
	encoded, err := audit.EncodeEvent(event, nil)
	if err != nil {
		return audit.EncodedEvent{}, partRestoreFailure(ctx, "encode the part change event", err)
	}
	return encoded, nil
}

func callAuditPartRestore(ctx context.Context, pool *pgxpool.Pool, m AuditPartRestoreManifest, encoded audit.EncodedEvent) (int64, error) {
	header, parts, err := encoded.HeaderAndParts()
	if err != nil {
		return 0, partRestoreFailure(ctx, "read the change event messages", err)
	}
	var inserted int64
	err = pool.QueryRow(ctx, auditPartRestoreStatement,
		m.OrgID, m.TargetEventID, m.SourceEventID, m.PartIndex, m.ManifestSHA256,
		m.TargetRowHash, m.TargetSeq, m.TargetShard,
		m.SourceRowHash, m.SourceSeq, m.SourceShard,
		encoded.EventID, header, parts,
	).Scan(&inserted)
	if err != nil {
		return 0, partRestoreFailure(ctx, fmt.Sprintf("restore part %d of event %s", m.PartIndex, m.TargetEventID), err)
	}
	return inserted, nil
}

func partRestoreFailure(ctx context.Context, step string, err error) error {
	wrapped := fmt.Errorf("%s: %w", step, err)
	slog.ErrorContext(ctx, "audit_part_restore.step_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
