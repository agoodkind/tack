package audit

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// RowSource visits matching ledger rows individually. Reader implements it in
// production; test sources generate large exports without retaining all rows.
type RowSource interface {
	StreamQuery(ctx context.Context, f QueryFilter, visit RowVisitor) error
}

// ExportManifest identifies one exported bundle. Its signature covers every
// field except Signature. The bundle consists of this manifest and its rows
// file.
type ExportManifest struct {
	ExportID uuid.UUID `json:"export_id"`
	OrgID    uuid.UUID `json:"org_id"`
	Oldest   time.Time `json:"oldest"`
	Latest   time.Time `json:"latest"`
	RowCount int       `json:"row_count"`
	// EventsFile specifies the signed rows file name, preventing substitution
	// of another export's file. Older manifests omit it and use a fixed name.
	EventsFile  string `json:"events_file,omitempty"`
	FileSHA256  string `json:"file_sha256"`
	SignatureBy string `json:"signing_key_id"`
	Signature   string `json:"signature"`
}

// Export writes matching rows to events-<export id>.jsonl and publishes their
// signed manifest as manifest.json. The caller supplies the ledger reader and
// Ed25519 signing key; verification uses the public key. A zero limit selects
// all matching rows, including for compliance exports and restore drills.
// A positive limit selects that many newest rows.
//
// Rows use a unique file per export and bounded streaming memory. Publication
// atomically replaces only the manifest. Concurrent exports can publish in
// either order without combining one export's manifest with another's rows.
// A failed export does not overwrite files in an already-published bundle.
func Export(
	ctx context.Context,
	reader RowSource,
	signer ed25519.PrivateKey,
	keyID string,
	filter QueryFilter,
	dir string,
) (*ExportManifest, error) {
	if reader == nil {
		return nil, errors.New("audit export: reader required")
	}

	if signer == nil || keyID == "" {
		return nil, errors.New("audit export: signing key required")
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("audit export mkdir: %w", err)
	}

	exportID := uuid.Must(uuid.NewV7())
	eventsName := exportEventsFileName(exportID)
	eventsPath := filepath.Join(dir, eventsName)

	activity := beginExportActivity(ctx, dir)
	defer activity.end()

	rowCount, fileDigest, err := writeExportRows(ctx, reader, filter, eventsPath)
	if err != nil {
		return nil, err
	}

	manifest := &ExportManifest{
		ExportID:    exportID,
		OrgID:       filter.OrgID,
		Oldest:      filter.Oldest,
		Latest:      filter.Latest,
		RowCount:    rowCount,
		EventsFile:  eventsName,
		FileSHA256:  fileDigest,
		SignatureBy: keyID,
		Signature:   "",
	}
	sig := ed25519.Sign(signer, exportSignableManifest(manifest))
	manifest.Signature = hex.EncodeToString(sig)

	if err := publishExportManifest(ctx, dir, exportID, manifest); err != nil {
		_ = os.Remove(eventsPath)
		return nil, err
	}

	// Persist the manifest rename before deleting superseded rows. Power loss
	// after durable deletion but before a durable rename could restore an old
	// manifest without its rows. Sync failure preserves all rows for recovery.
	if err := syncExportDirectory(dir); err != nil {
		return nil, err
	}

	// Release writer activity before trying exclusive reclamation.
	activity.end()
	reclaimAbandonedExportFiles(ctx, dir)

	return manifest, nil
}

// publishExportManifest synchronizes a staged manifest before atomically
// replacing the published name. It does not unlink the existing manifest
// first. Rename failures preserve the published file and return an error.
func publishExportManifest(
	ctx context.Context,
	dir string,
	exportID uuid.UUID,
	manifest *ExportManifest,
) error {
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("audit export marshal manifest: %w", err)
	}

	published := filepath.Join(dir, exportManifestFile)
	staged := stagedExportPath(published, exportID)
	if err := writeSyncedFile(staged, manifestBytes, 0o600); err != nil {
		_ = os.Remove(staged)
		slog.ErrorContext(
			ctx,
			"audit.export.publish_failed",
			slog.String("path", staged),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("audit export write manifest: %w", err)
	}

	if err := os.Rename(staged, published); err != nil {
		_ = os.Remove(staged)
		slog.ErrorContext(
			ctx,
			"audit.export.publish_failed",
			slog.String("path", published),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("audit export publish manifest: %w", err)
	}

	return nil
}

// exportSignableManifest encodes all signed fields for both signing and
// verification. Keep events_file last and omit it when empty to reproduce
// older manifests' signed bytes while signing each newer rows file name.
func exportSignableManifest(manifest *ExportManifest) []byte {
	signable, _ := json.Marshal(
		struct {
			ExportID    uuid.UUID `json:"export_id"`
			OrgID       uuid.UUID `json:"org_id"`
			Oldest      time.Time `json:"oldest"`
			Latest      time.Time `json:"latest"`
			RowCount    int       `json:"row_count"`
			FileSHA256  string    `json:"file_sha256"`
			SignatureBy string    `json:"signing_key_id"`
			EventsFile  string    `json:"events_file,omitempty"`
		}{
			ExportID:    manifest.ExportID,
			OrgID:       manifest.OrgID,
			Oldest:      manifest.Oldest,
			Latest:      manifest.Latest,
			RowCount:    manifest.RowCount,
			FileSHA256:  manifest.FileSHA256,
			SignatureBy: manifest.SignatureBy,
			EventsFile:  manifest.EventsFile,
		},
	)

	return signable
}
