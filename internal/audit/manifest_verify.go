package audit

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// ManifestVerification contains safe metadata from a verified read manifest.
type ManifestVerification struct {
	DeclaredCount    uint64 `json:"declared_count"`
	IdentityCount    uint64 `json:"identity_count"`
	DeclaredParts    uint32 `json:"declared_parts"`
	PartCount        uint32 `json:"part_count"`
	DeclaredDigest   string `json:"declared_digest"`
	RecomputedDigest string `json:"recomputed_digest"`
	PayloadVerified  bool   `json:"payload_verified"`
}

// VerifyManifest checks the stored payloads against one organization's event.
func (r *Reader) VerifyManifest(ctx context.Context, orgID, eventID uuid.UUID) (ManifestVerification, error) {
	var result ManifestVerification
	rows, err := r.pool.Rows(r.pool.unsanitized.Query(ctx, readStatement("manifest_evidence"), orgID, eventID, EventPartMaxBytes+1))
	if err != nil {
		return result, fmt.Errorf("cannot read manifest evidence for event %s", eventID)
	}
	var raw []byte
	if !rows.Next() {
		rows.Close()
		if rows.Err() != nil {
			return result, fmt.Errorf("cannot read manifest evidence for event %s", eventID)
		}
		return result, fmt.Errorf("event %s was not found in the requested organization", eventID)
	}
	if err := rows.Scan(&raw); err != nil {
		rows.Close()
		return result, fmt.Errorf("cannot decode manifest evidence for event %s", eventID)
	}
	if rows.Next() {
		rows.Close()
		return result, fmt.Errorf("event %s has multiple declarations in the requested organization", eventID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, fmt.Errorf("cannot read manifest evidence for event %s", eventID)
	}
	var read *ReadEvidence
	if len(raw) > EventPartMaxBytes {
		return result, fmt.Errorf("event %s declares oversized read evidence", eventID)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&read); err != nil || read == nil || !decoderAtEnd(decoder) {
		return result, fmt.Errorf("event %s does not declare valid read evidence", eventID)
	}
	if !isSHA256Hex(read.ManifestSHA256) {
		return result, fmt.Errorf("event %s does not declare a valid manifest digest", eventID)
	}
	rows, err = r.pool.Rows(r.pool.unsanitized.Query(ctx, readStatement("manifest_payloads"), orgID, eventID, EventPartMaxBytes+1))
	if err != nil {
		return result, fmt.Errorf("cannot read manifest parts for event %s", eventID)
	}
	defer rows.Close()
	validator := newManifestValidator()
	for rows.Next() {
		part := ManifestPart{Index: 0, Digest: "", Payload: nil}
		if err := rows.Scan(&part.Index, &part.Digest, &part.Payload); err != nil {
			return result, fmt.Errorf("cannot decode a manifest part for event %s", eventID)
		}
		if violation := validator.add(part); violation != "" {
			return result, fmt.Errorf("event %s %s", eventID, violation)
		}
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("cannot read manifest parts for event %s", eventID)
	}
	if violation := validator.finish(read, true); violation != "" {
		return result, fmt.Errorf("event %s %s", eventID, violation)
	}
	return ManifestVerification{
		DeclaredCount: read.Count, IdentityCount: validator.identities,
		DeclaredParts: read.ManifestParts, PartCount: validator.parts,
		DeclaredDigest: read.ManifestSHA256, RecomputedDigest: hex.EncodeToString(validator.root.Sum(nil)), PayloadVerified: true,
	}, nil
}
