package integration_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

func (p *partRestorePipeline) requireProtectedPayload(t *testing.T, reference uuid.UUID) {
	t.Helper()
	var stored []byte
	var subjectID, orgID uuid.UUID
	if err := p.ledger.admin.QueryRow(t.Context(), readTestSQL(t, "part_restore_protected_payload.sql"), reference).Scan(&stored, &subjectID, &orgID); err != nil {
		t.Fatalf("read the protected payload %s: %v", reference, err)
	}
	var encoded string
	if err := json.Unmarshal(stored, &encoded); err != nil {
		t.Fatalf("decode the protected payload value: %v", err)
	}
	reconstructed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode the base64 payload: %v", err)
	}
	parts, err := p.ledger.reader.ManifestParts(t.Context(), p.org, p.target)
	if err != nil || len(parts) != 1 {
		t.Fatalf("read the restored part: %d parts, %v", len(parts), err)
	}
	if !bytes.Equal(reconstructed, parts[0].Payload) || subjectID != p.target || orgID != p.org {
		t.Fatalf("protected payload: %d bytes equal %v, subject %s, org %s, want the stored part of %s in org %s",
			len(reconstructed), bytes.Equal(reconstructed, parts[0].Payload), subjectID, orgID, p.target, p.org)
	}
}

func (p *partRestorePipeline) requireObservedEvidence(t *testing.T, row audit.Row) {
	t.Helper()
	var observed struct {
		PartDigest    string `json:"part_digest"`
		PayloadBytes  int    `json:"payload_bytes"`
		TargetSeq     int64  `json:"target_seq"`
		TargetShard   int16  `json:"target_shard"`
		TargetRowHash []byte `json:"target_row_hash"`
	}
	manifest := p.manifest
	if err := json.Unmarshal(row.Extra, &observed); err != nil || observed.PartDigest != manifest.ManifestSHA256 || observed.PayloadBytes != len(p.payload) ||
		observed.TargetSeq != manifest.TargetSeq || observed.TargetShard != manifest.TargetShard || !bytes.Equal(observed.TargetRowHash, manifest.TargetRowHash) {
		t.Fatalf("step result evidence = %s, error %v, want the stored part digest, %d payload bytes, and the unchanged target seq, shard, and row hash", row.Extra, err, len(p.payload))
	}
}

func (p *partRestorePipeline) requireNoPayloadText(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, string(p.payload)) || strings.Contains(text, base64.StdEncoding.EncodeToString(p.payload)) {
		t.Fatal("text contains payload bytes")
	}
}
