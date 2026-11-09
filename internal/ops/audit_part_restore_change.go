package ops

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

//go:embed audit_part_restore.sql
var auditPartRestoreStatement string

const auditPartRestoreSQLState = "TK620"

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
	uri := audit.EventURIPrefix + eventID.String() + auditEventPartURISegment + strconv.FormatInt(partIndex, 10)
	target := audit.ResourceTarget(uri)
	target.NodeType = audit.TargetAuditEventPart
	target.ID = eventID
	return target
}

// BuildAuditPartChange builds and encodes the change event that records the
// restore of one manifest part.
//
// It derives the event ID from the manifest file
// digest and the part target. A rerun with the same manifest produces the same
// event ID. The new value of the change records the organization, target event,
// part index, and manifest digest. The change stores the payload as a protected
// value with the target event as its subject. The event extra records
// the source event ID and the manifest file digest. BuildAuditPartChange
// requires the mutation context of the running command.
//
// The change target URI uses this layout when the manifest restores part 0:
//
//	tack:audit-event/<target_event_id>/part/0
func BuildAuditPartChange(
	ctx context.Context,
	manifest AuditPartRestoreManifest,
	manifestFileDigest string,
	payload []byte,
) (audit.EncodedEvent, error) {
	operation, err := audit.RequireMutationContext(ctx)
	if err != nil {
		return audit.EncodedEvent{}, partRestoreFailure(ctx, "cannot read the part restore operation", err)
	}
	operation.Context.OrgID = manifest.OrgID

	provenance, err := json.Marshal(auditPartProvenance{
		SourceEventID: manifest.SourceEventID, ManifestFileSHA256: manifestFileDigest,
	})
	if err != nil {
		return audit.EncodedEvent{}, partRestoreFailure(ctx, "cannot encode the part provenance", err)
	}
	operation.Extra = provenance
	target := auditPartTarget(manifest.TargetEventID, manifest.PartIndex)
	changeID := uuid.NewSHA1(auditPartRestoreNamespace, []byte(manifestFileDigest+"/"+target.URI))
	payloadRef := uuid.NewSHA1(auditPartRestoreNamespace, []byte(changeID.String()+"/payload"))
	after, err := json.Marshal(auditPartRecord{
		OrgID: manifest.OrgID, EventID: manifest.TargetEventID,
		PartIndex: manifest.PartIndex, Digest: manifest.ManifestSHA256,
		Payload: audit.ProtectedReference{PIIRef: payloadRef},
	})
	if err != nil {
		return audit.EncodedEvent{}, partRestoreFailure(ctx, "cannot encode the restored part record", err)
	}
	change, err := audit.NewChange(changeID, target, nil, after)
	if err != nil {
		return audit.EncodedEvent{}, partRestoreFailure(ctx, "cannot build the part change", err)
	}
	protectedValue := json.RawMessage(`"` + base64.StdEncoding.EncodeToString(payload) + `"`)
	change.Protected = []audit.ProtectedValue{{Ref: payloadRef, SubjectID: target.ID, Value: protectedValue}}
	event, err := audit.ChangeEvent(operation, change, clock.Now())
	if err != nil {
		return audit.EncodedEvent{}, partRestoreFailure(ctx, "cannot build the part change event", err)
	}
	encoded, err := audit.EncodeEvent(event, nil)
	if err != nil {
		return audit.EncodedEvent{}, partRestoreFailure(ctx, "cannot encode the part change event", err)
	}
	return encoded, nil
}

func callAuditPartRestore(
	ctx context.Context,
	pool *pgxpool.Pool,
	manifest AuditPartRestoreManifest,
	encoded audit.EncodedEvent,
) error {
	header, parts, err := encoded.HeaderAndParts()
	if err != nil {
		return partRestoreFailure(ctx, "cannot read the change event messages", err)
	}

	_, err = pool.Exec(ctx, auditPartRestoreStatement,
		manifest.OrgID, manifest.TargetEventID, manifest.SourceEventID, manifest.ManifestSHA256,
		manifest.TargetRowHash, manifest.TargetSeq, manifest.TargetShard,
		manifest.SourceRowHash, manifest.SourceSeq, manifest.SourceShard,
		encoded.EventID, header, parts,
	)

	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == auditPartRestoreSQLState {
		return refusePartRestore(ctx, "the restore function refused: "+postgresError.Message)
	}

	if err != nil {
		return partRestoreFailure(ctx, "cannot restore the part of event "+manifest.TargetEventID.String(), err)
	}

	return nil
}
