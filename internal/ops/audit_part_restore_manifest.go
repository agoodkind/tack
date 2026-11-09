package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

const (
	auditPartRestoreSchemaVersion = 1
	auditPartRestoreTicket        = "TACK-620"
	auditPartRestoreShardCount    = 256
)

var errAuditPartRestoreManifest = errors.New("the part restore manifest is not valid")

var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// AuditPartRestoreManifest is the frozen request that the one-time TACK-620
// part restore command reads, decodes, and validates.
type AuditPartRestoreManifest struct {
	SchemaVersion     int       `json:"schema_version"`
	Ticket            string    `json:"ticket"`
	OrgID             uuid.UUID `json:"org_id"`
	TargetEventID     uuid.UUID `json:"target_event_id"`
	SourceEventID     uuid.UUID `json:"source_event_id"`
	PartIndex         int64     `json:"part_index"`
	ManifestSHA256    string    `json:"manifest_sha256"`
	DeclaredParts     uint32    `json:"declared_parts"`
	IdentityCount     uint64    `json:"identity_count"`
	TargetSeq         int64     `json:"target_seq"`
	TargetShard       int16     `json:"target_shard"`
	TargetHashVersion int16     `json:"target_hash_version"`
	TargetRowHash     []byte    `json:"target_row_hash"`
	SourceSeq         int64     `json:"source_seq"`
	SourceShard       int16     `json:"source_shard"`
	SourceRowHash     []byte    `json:"source_row_hash"`
}

var auditPartRestoreRequiredMembers = []string{
	"schema_version", "ticket", "org_id", "target_event_id", "source_event_id", "part_index", "manifest_sha256",
	"declared_parts", "identity_count", "target_seq", "target_shard", "target_hash_version",
	"target_row_hash", "source_seq", "source_shard", "source_row_hash",
}

func readAuditPartRestoreManifest(
	ctx context.Context,
	repairDir, path, wantDigest string,
) (AuditPartRestoreManifest, string, error) {
	directory, err := openRepairDirectory(ctx, repairDir)
	if err != nil {
		return AuditPartRestoreManifest{}, "", manifestRefused(ctx, err.Error())
	}

	err = directory.require(ctx, "manifest", path)
	if err != nil {
		return AuditPartRestoreManifest{}, "", manifestRefused(ctx, err.Error())
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return AuditPartRestoreManifest{}, "", manifestRefused(ctx,
			fmt.Sprintf("cannot read part restore manifest %q: %v", path, err))
	}
	fileDigest := audit.SHA256Hex(content)
	if fileDigest != wantDigest {
		return AuditPartRestoreManifest{}, "", manifestRefused(ctx,
			fmt.Sprintf("manifest file digest %s does not equal --manifest-sha256 %s", fileDigest, wantDigest))
	}
	err = requireManifestMembers(ctx, content)
	if err != nil {
		return AuditPartRestoreManifest{}, "", err
	}

	var manifest AuditPartRestoreManifest
	err = decodeStrictJSON(ctx, content, &manifest)
	if err != nil {
		return AuditPartRestoreManifest{}, "", manifestRefused(ctx, err.Error())
	}
	err = manifest.validate()
	if err != nil {
		return AuditPartRestoreManifest{}, "", manifestRefused(ctx, err.Error())
	}
	return manifest, fileDigest, nil
}

func requireManifestMembers(ctx context.Context, content []byte) error {
	var members map[string]json.RawMessage
	err := json.Unmarshal(content, &members)
	if err != nil {
		return manifestRefused(ctx, "cannot decode members: "+err.Error())
	}

	for _, name := range auditPartRestoreRequiredMembers {
		raw, found := members[name]
		if !found || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return manifestRefused(ctx, "member "+name+" is missing or null")
		}
	}
	return nil
}

func (m AuditPartRestoreManifest) validate() error {
	switch {
	case m.SchemaVersion != auditPartRestoreSchemaVersion:
		return fmt.Errorf("%w %d is not %d", errAuditPartSchemaVersion, m.SchemaVersion, auditPartRestoreSchemaVersion)
	case m.Ticket != auditPartRestoreTicket:
		return fmt.Errorf("%w %q is not %s", errAuditPartTicket, m.Ticket, auditPartRestoreTicket)
	case m.OrgID == uuid.Nil || m.TargetEventID == uuid.Nil || m.SourceEventID == uuid.Nil:
		return errAuditPartMissingIdentities
	case m.TargetEventID == m.SourceEventID:
		return errAuditPartSameEvent
	case m.PartIndex != 0:
		return fmt.Errorf("%w %d is not 0", errAuditPartIndex, m.PartIndex)
	case !sha256HexPattern.MatchString(m.ManifestSHA256):
		return errAuditPartManifestDigest
	case m.DeclaredParts != 1 || m.IdentityCount == 0:
		return errAuditPartManifestCounts
	case m.TargetSeq <= 0 || m.SourceSeq <= 0:
		return errAuditPartSequences
	case m.TargetHashVersion <= 0:
		return errAuditPartHashVersion
	case len(m.TargetRowHash) == 0 || len(m.SourceRowHash) == 0:
		return errAuditPartRowHashes
	case m.TargetShard < 0 || m.TargetShard >= auditPartRestoreShardCount ||
		m.SourceShard < 0 || m.SourceShard >= auditPartRestoreShardCount:
		return errAuditPartShards
	}

	return nil
}

func manifestRefused(ctx context.Context, reason string) error {
	return refuseWith(ctx, errAuditPartRestoreManifest, reason)
}
