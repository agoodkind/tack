package ops

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
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

// BuildAuditPartChange builds and encodes the change event that records the
// restore of one manifest part. It derives the event ID from the manifest file
// digest and the part target. A rerun with the same manifest produces the same
// event ID. The new value of the change records the organization, target event,
// part index, and manifest digest. The change stores the payload as a protected
// value with the target event as its subject. The event extra records the source
// event ID and the manifest file digest. BuildAuditPartChange requires the
// mutation context of the running command.
func BuildAuditPartChange(ctx context.Context, m AuditPartRestoreManifest, manifestFileDigest string, payload []byte) (audit.EncodedEvent, error) {
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
	target := auditPartTarget(m.TargetEventID, m.PartIndex)
	changeID := uuid.NewSHA1(auditPartRestoreNamespace, []byte(manifestFileDigest+"/"+target.URI))
	payloadRef := uuid.NewSHA1(auditPartRestoreNamespace, []byte(changeID.String()+"/payload"))
	after, err := json.Marshal(auditPartRecord{
		OrgID: m.OrgID, EventID: m.TargetEventID, PartIndex: m.PartIndex, Digest: m.ManifestSHA256,
		Payload: audit.ProtectedReference{PIIRef: payloadRef},
	})
	if err != nil {
		return audit.EncodedEvent{}, partRestoreFailure(ctx, "encode the restored part record", err)
	}
	change, err := audit.NewChange(changeID, target, nil, after)
	if err != nil {
		return audit.EncodedEvent{}, partRestoreFailure(ctx, "build the part change", err)
	}
	protectedValue := json.RawMessage(`"` + base64.StdEncoding.EncodeToString(payload) + `"`)
	change.Protected = []audit.ProtectedValue{{Ref: payloadRef, SubjectID: target.ID, Value: protectedValue}}
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

func callAuditPartRestore(ctx context.Context, pool *pgxpool.Pool, m AuditPartRestoreManifest, encoded audit.EncodedEvent) error {
	header, parts, err := encoded.HeaderAndParts()
	if err != nil {
		return partRestoreFailure(ctx, "read the change event messages", err)
	}
	_, err = pool.Exec(ctx, auditPartRestoreStatement,
		m.OrgID, m.TargetEventID, m.SourceEventID, m.ManifestSHA256,
		m.TargetRowHash, m.TargetSeq, m.TargetShard,
		m.SourceRowHash, m.SourceSeq, m.SourceShard,
		encoded.EventID, header, parts,
	)
	if err != nil {
		return partRestoreFailure(ctx, "restore the part of event "+m.TargetEventID.String(), err)
	}
	return nil
}
