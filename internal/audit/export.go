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

// RowSource supplies each matching ledger row individually for an export.
// [Reader] implements the interface in production. Tests can supply a large
// ledger through the interface to check that export memory does not grow with
// the row count.
type RowSource interface {
	StreamQuery(ctx context.Context, f QueryFilter, visit RowVisitor) error
}

// ExportManifest records a bundle's metadata. Its signature covers every field
// except Signature. A bundle consists of the manifest and the rows file it
// selects; other directory entries do not belong to the bundle.
type ExportManifest struct {
	ExportID uuid.UUID `json:"export_id"`
	OrgID    uuid.UUID `json:"org_id"`
	Oldest   time.Time `json:"oldest"`
	Latest   time.Time `json:"latest"`
	RowCount int       `json:"row_count"`
	// EventsFile selects the rows file, and the signature covers that selection.
	// Changing it to another export's rows invalidates the signature. Older bundles
	// omit the field and use the fixed filename from their original format.
	EventsFile  string `json:"events_file,omitempty"`
	FileSHA256  string `json:"file_sha256"`
	SignatureBy string `json:"signing_key_id"`
	Signature   string `json:"signature"`
}

// Export streams matching ledger events into a signed bundle directory.
//
// The caller supplies a [RowSource] and the notarizer's Ed25519 signing key.
// Verification uses the public key. The function returns an [ExportManifest]
// that the caller can render.
//
//	<dir>/events-<export id>.jsonl   newline-delimited Row records
//	<dir>/manifest.json              {export_id, org_id, time_range, row_count,
//	                                  events_file, file_sha256, signature,
//	                                  signing_key_id}
//
// The function writes and releases each row individually while accumulating the
// row count and file digest. Ledger size does not impose a row limit. A zero
// filter.Limit exports every row in the range for compliance exports and the
// restore drill's chain check. A positive limit selects that many newest rows.
//
// Each export writes a unique rows filename and never renames it. One rename
// replaces the manifest to publish the bundle. Concurrent exports replace only
// that shared manifest; the last replacement selects that export's signed rows.
// An export that fails before publication preserves the existing bundle because
// it does not rewrite published files in place.
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
	// The manifest rename changes the directory and requires a directory sync. If
	// an unlink persists before that rename, power loss can leave the previous
	// manifest without its rows. Preserve files when the sync fails: publication
	// has succeeded, but directory durability remains unproven.
	if err := syncExportDirectory(dir); err != nil {
		return nil, err
	}

	// Reclaim runs after publication and after this export stops counting as
	// active. It considers the published bundle and rows from exports that another
	// export superseded or that stopped before publication.
	activity.end()
	reclaimAbandonedExportFiles(ctx, dir)
	return manifest, nil
}

// publishExportManifest writes a staged manifest and atomically renames it over
// the published filename. It does not unlink the existing manifest first. An
// unlink followed by a failed rename or crash would remove the usable bundle. A
// failed rename preserves the previous publication and returns an error.
//
// The function persists staged bytes before the rename. A rename publishes a
// directory entry without persisting file contents. Publishing unsynced bytes
// could leave an empty or incomplete signed compliance artifact after power
// loss.
func publishExportManifest(ctx context.Context, dir string, exportID uuid.UUID, manifest *ExportManifest) error {
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("audit export marshal manifest: %w", err)
	}
	published := filepath.Join(dir, exportManifestFile)
	staged := stagedExportPath(published, exportID)
	if err := writeSyncedFile(staged, manifestBytes, 0o600); err != nil {
		_ = os.Remove(staged)
		slog.ErrorContext(ctx, "audit.export.publish_failed",
			slog.String("path", staged), slog.String("err", err.Error()))
		return fmt.Errorf("audit export write manifest: %w", err)
	}
	if err := os.Rename(staged, published); err != nil {
		_ = os.Remove(staged)
		slog.ErrorContext(ctx, "audit.export.publish_failed",
			slog.String("path", published), slog.String("err", err.Error()))
		return fmt.Errorf("audit export publish manifest: %w", err)
	}
	return nil
}

// exportSignableManifest serializes every manifest field except Signature.
// Signing and verification use this function to avoid different projections.
//
// The events_file field remains last and omits empty values. Older manifests
// retain their original signing bytes, and current manifests sign the filename
// that selects their rows.
func exportSignableManifest(manifest *ExportManifest) []byte {
	signable, _ := json.Marshal(struct {
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
	})
	return signable
}
