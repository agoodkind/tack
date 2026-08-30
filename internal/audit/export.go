package audit

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// exportWriteBufferBytes buffers the bundle's writes. Encoding straight to the
// file descriptor costs one write syscall per row, which a ledger-sized export
// pays hundreds of thousands of times.
const exportWriteBufferBytes = 1 << 20

// RowSource is the ledger read an export needs: every row the filter matches,
// handed over one at a time. *Reader is the production implementation, and the
// interface is what lets an export be driven by a row supply large enough to
// prove the export does not grow with it.
type RowSource interface {
	StreamQuery(ctx context.Context, f QueryFilter, visit RowVisitor) error
}

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
//
// The rows are streamed: each one is written and released as it is read, and
// the row count and the file digest are both accumulated as that happens, so
// there is no ledger size this refuses. A zero filter.Limit exports every row
// in the range, which is what a compliance export and the restore drill's chain
// check both ask for; a positive limit exports that many of the newest rows.
func Export(ctx context.Context, reader RowSource, signer ed25519.PrivateKey, keyID string, filter QueryFilter, dir string) (*ExportManifest, error) {
	if reader == nil {
		return nil, errors.New("audit export: reader required")
	}
	if signer == nil || keyID == "" {
		return nil, errors.New("audit export: signing key required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("audit export mkdir: %w", err)
	}

	rowCount, fileDigest, err := writeExportRows(ctx, reader, filter, filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil, err
	}

	manifest := &ExportManifest{
		ExportID:    uuid.Must(uuid.NewV7()),
		OrgID:       filter.OrgID,
		Oldest:      filter.Oldest,
		Latest:      filter.Latest,
		RowCount:    rowCount,
		FileSHA256:  fileDigest,
		SignatureBy: keyID,
		Signature:   "",
	}
	sig := ed25519.Sign(signer, exportSignableManifest(manifest))
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

// writeExportRows streams the filter's rows straight into the bundle file and
// reports how many it wrote and the digest of what it wrote.
//
// Nothing here grows with the export: a row is encoded and released as it
// arrives, the digest folds in the same bytes the file receives, and the count
// is a counter. Collecting the rows into a slice first is what made a
// production-sized org unexportable, because the slice, not the file, is what
// had to fit in memory.
func writeExportRows(ctx context.Context, source RowSource, filter QueryFilter, path string) (int, string, error) {
	file, err := os.Create(path)
	if err != nil {
		slog.ErrorContext(ctx, "audit.export.open_failed",
			slog.String("path", path), slog.String("err", err.Error()))
		return 0, "", fmt.Errorf("audit export open: %w", err)
	}
	fileDigest := sha256.New()
	buffered := bufio.NewWriterSize(file, exportWriteBufferBytes)
	encoder := json.NewEncoder(io.MultiWriter(buffered, fileDigest))

	rowCount := 0
	streamErr := source.StreamQuery(ctx, filter, func(row Row) error {
		if encodeErr := encoder.Encode(row); encodeErr != nil {
			return fmt.Errorf("encode %s: %w", row.EventID, encodeErr)
		}
		rowCount++
		return nil
	})
	if streamErr != nil {
		_ = file.Close()
		return 0, "", fmt.Errorf("audit export query: %w", streamErr)
	}
	// The buffer holds the tail of the file, and the digest has already counted
	// those bytes. Skipping the flush would sign a digest of rows the bundle
	// does not carry, so the failure has to reach the caller rather than be
	// swallowed by the deferred close.
	if flushErr := buffered.Flush(); flushErr != nil {
		_ = file.Close()
		return 0, "", fmt.Errorf("audit export flush: %w", flushErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		return 0, "", fmt.Errorf("audit export close: %w", closeErr)
	}
	return rowCount, hex.EncodeToString(fileDigest.Sum(nil)), nil
}

// exportSignableManifest renders the manifest fields the signature covers. The
// signature is over everything but itself, so verification re-renders the same
// shape from the manifest it read.
func exportSignableManifest(manifest *ExportManifest) []byte {
	signable, _ := json.Marshal(struct {
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
	return signable
}
