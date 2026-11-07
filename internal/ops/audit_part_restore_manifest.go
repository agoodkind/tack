package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"

	"github.com/google/uuid"
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
	PartDigest        string    `json:"part_digest"`
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

func readAuditPartRestoreManifest(ctx context.Context, repairDir, path, wantDigest string) (AuditPartRestoreManifest, string, error) {
	directory, err := openRepairDirectory(ctx, repairDir)
	if err != nil {
		return AuditPartRestoreManifest{}, "", err
	}
	if err := directory.require(ctx, "manifest", path); err != nil {
		return AuditPartRestoreManifest{}, "", err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		slog.ErrorContext(ctx, "audit_part_restore.manifest_read_failed", slog.String("path", path), slog.String("err", err.Error()))
		return AuditPartRestoreManifest{}, "", fmt.Errorf("read part restore manifest %q: %w", path, err)
	}
	sum := sha256.Sum256(content)
	fileDigest := hex.EncodeToString(sum[:])
	if fileDigest != wantDigest {
		return AuditPartRestoreManifest{}, "", manifestRefused(ctx,
			fmt.Sprintf("manifest file digest %s does not equal --manifest-sha256 %s", fileDigest, wantDigest))
	}
	var manifest AuditPartRestoreManifest
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return AuditPartRestoreManifest{}, "", manifestRefused(ctx, "decode: "+err.Error())
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return AuditPartRestoreManifest{}, "", manifestRefused(ctx, "data after the first JSON value")
	}
	if err := manifest.validate(); err != nil {
		return AuditPartRestoreManifest{}, "", manifestRefused(ctx, err.Error())
	}
	return manifest, fileDigest, nil
}

func (m AuditPartRestoreManifest) validate() error {
	switch {
	case m.SchemaVersion != auditPartRestoreSchemaVersion:
		return fmt.Errorf("schema_version %d is not %d", m.SchemaVersion, auditPartRestoreSchemaVersion)
	case m.Ticket != auditPartRestoreTicket:
		return fmt.Errorf("ticket %q is not %s", m.Ticket, auditPartRestoreTicket)
	case m.OrgID == uuid.Nil || m.TargetEventID == uuid.Nil || m.SourceEventID == uuid.Nil:
		return errors.New("org_id, target_event_id, and source_event_id must be set")
	case m.TargetEventID == m.SourceEventID:
		return errors.New("target_event_id and source_event_id must differ")
	case m.PartIndex != 0:
		return fmt.Errorf("part_index %d is not 0", m.PartIndex)
	case !sha256HexPattern.MatchString(m.ManifestSHA256) || m.PartDigest != m.ManifestSHA256:
		return errors.New("manifest_sha256 must be 64 lowercase hex digits and equal part_digest")
	case m.DeclaredParts != 1 || m.IdentityCount == 0:
		return errors.New("declared_parts must be 1 and identity_count must be positive")
	case len(m.TargetRowHash) == 0 || len(m.SourceRowHash) == 0:
		return errors.New("target_row_hash and source_row_hash must be set")
	case m.TargetShard < 0 || m.TargetShard >= auditPartRestoreShardCount || m.SourceShard < 0 || m.SourceShard >= auditPartRestoreShardCount:
		return errors.New("target_shard and source_shard must be in 0..255")
	}
	return nil
}

func manifestRefused(ctx context.Context, reason string) error {
	return refuseWith(ctx, errAuditPartRestoreManifest, reason)
}
