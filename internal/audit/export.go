package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// Export streams the events that match filter into a directory layout:
//
//	<dir>/events.jsonl       newline-delimited Row records
//	<dir>/manifest.json      {export_id, org_id, time_range, row_count,
//	                          file_sha256, signature, signing_key_id}
//
// Caller supplies the audit Reader and the same Ed25519 signing key the
// notarizer uses; verification reuses the public half. Returns the manifest
// as a Go struct so the caller can render it.
type ExportManifest struct {
	ExportID    uuid.UUID `json:"export_id"`
	OrgID       uuid.UUID `json:"org_id"`
	Oldest      time.Time `json:"oldest"`
	Latest      time.Time `json:"latest"`
	RowCount    int       `json:"row_count"`
	FileSHA256  string    `json:"file_sha256"`
	SignatureBy string    `json:"signing_key_id"`
	Signature   string    `json:"signature"`
}

// Export writes events.jsonl + manifest.json under dir and returns the
// manifest.
func Export(ctx context.Context, reader *Reader, signer ed25519.PrivateKey, keyID string, filter QueryFilter, dir string) (*ExportManifest, error) {
	if reader == nil {
		return nil, errors.New("audit export: reader required")
	}
	if signer == nil || keyID == "" {
		return nil, errors.New("audit export: signing key required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("audit export mkdir: %w", err)
	}

	rows, err := reader.Query(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("audit export query: %w", err)
	}

	jsonlPath := filepath.Join(dir, "events.jsonl")
	f, err := os.Create(jsonlPath)
	if err != nil {
		return nil, fmt.Errorf("audit export open: %w", err)
	}
	hasher := sha256.New()
	writer := io.MultiWriter(f, hasher)
	enc := json.NewEncoder(writer)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("audit export encode: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("audit export close: %w", err)
	}

	manifest := &ExportManifest{
		ExportID:    uuid.Must(uuid.NewV7()),
		OrgID:       filter.OrgID,
		Oldest:      filter.Oldest,
		Latest:      filter.Latest,
		RowCount:    len(rows),
		FileSHA256:  hex.EncodeToString(hasher.Sum(nil)),
		SignatureBy: keyID,
	}
	manifestForSign, _ := json.Marshal(struct {
		ExportID    uuid.UUID `json:"export_id"`
		OrgID       uuid.UUID `json:"org_id"`
		Oldest      time.Time `json:"oldest"`
		Latest      time.Time `json:"latest"`
		RowCount    int       `json:"row_count"`
		FileSHA256  string    `json:"file_sha256"`
		SignatureBy string    `json:"signing_key_id"`
	}{
		ExportID:    manifest.ExportID,
		OrgID:       manifest.OrgID,
		Oldest:      manifest.Oldest,
		Latest:      manifest.Latest,
		RowCount:    manifest.RowCount,
		FileSHA256:  manifest.FileSHA256,
		SignatureBy: manifest.SignatureBy,
	})
	sig := ed25519.Sign(signer, manifestForSign)
	manifest.Signature = hex.EncodeToString(sig)

	mfPath := filepath.Join(dir, "manifest.json")
	mfBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("audit export marshal manifest: %w", err)
	}
	if err := os.WriteFile(mfPath, mfBytes, 0o600); err != nil {
		return nil, fmt.Errorf("audit export write manifest: %w", err)
	}
	return manifest, nil
}
