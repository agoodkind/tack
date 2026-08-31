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

// RowSource is the ledger read an export needs: every row the filter matches,
// handed over one at a time. *Reader is the production implementation, and the
// interface is what lets an export be driven by a row supply large enough to
// prove the export does not grow with it.
type RowSource interface {
	StreamQuery(ctx context.Context, f QueryFilter, visit RowVisitor) error
}

// ExportManifest describes one exported bundle and is signed over every field
// but its own signature. A bundle is this manifest plus the rows file it names,
// and nothing else in the directory is part of it.
type ExportManifest struct {
	ExportID uuid.UUID `json:"export_id"`
	OrgID    uuid.UUID `json:"org_id"`
	Oldest   time.Time `json:"oldest"`
	Latest   time.Time `json:"latest"`
	RowCount int       `json:"row_count"`
	// EventsFile names the rows file this manifest describes, and the signature
	// covers it. Without that binding a signed manifest could be repointed at
	// another export's rows; with it, the pair either belongs together or fails
	// to verify. It is empty only on a bundle written before the manifest
	// carried the name, whose format fixed the name instead.
	EventsFile  string `json:"events_file,omitempty"`
	FileSHA256  string `json:"file_sha256"`
	SignatureBy string `json:"signing_key_id"`
	Signature   string `json:"signature"`
}

// Export streams the events that match filter into a directory layout:
//
//	<dir>/events-<export id>.jsonl   newline-delimited Row records
//	<dir>/manifest.json              {export_id, org_id, time_range, row_count,
//	                                  events_file, file_sha256, signature,
//	                                  signing_key_id}
//
// Caller supplies the audit Reader and the same Ed25519 signing key the
// notarizer uses; verification reuses the public half. Returns the manifest as
// a Go struct so the caller can render it.
//
// The rows are streamed: each one is written and released as it is read, and
// the row count and the file digest are both accumulated as that happens, so
// there is no ledger size this refuses. A zero filter.Limit exports every row
// in the range, which is what a compliance export and the restore drill's chain
// check both ask for; a positive limit exports that many of the newest rows.
//
// Publishing is the single rename that replaces the manifest. The rows are
// written under a name no other export can use and are never renamed, so the
// manifest is the only mutable name in the directory and the pair it names is
// always its own. Two exports into one directory therefore race to replace one
// file: whichever lands second is the bundle, and either outcome is a manifest
// beside the rows it was signed for. A re-export that fails partway leaves the
// bundle already in the directory exactly as it was, because nothing published
// is written in place.
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

	// The reclaim runs after the manifest is published and after this export
	// stops counting as active, so what it sees is a settled directory: the
	// bundle that is now published, plus the rows of every export that was
	// superseded or died before it could publish.
	activity.end()
	reclaimAbandonedExportFiles(ctx, dir)
	return manifest, nil
}

// publishExportManifest writes the manifest and renames it over the published
// name, which replaces the file there atomically. Nothing is unlinked first:
// removing the manifest up front would destroy the only thing that makes the
// directory readable as a bundle, and a rename that then failed, or a crash
// before it ran, would leave that loss permanent. A failure of the rename
// leaves the directory exactly as it was and reports it, which is why the error
// reaches the caller rather than being logged and swallowed.
func publishExportManifest(ctx context.Context, dir string, exportID uuid.UUID, manifest *ExportManifest) error {
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("audit export marshal manifest: %w", err)
	}
	published := filepath.Join(dir, exportManifestFile)
	staged := stagedExportPath(published, exportID)
	if err := os.WriteFile(staged, manifestBytes, 0o600); err != nil {
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

// exportSignableManifest renders the manifest fields the signature covers. The
// signature is over everything but itself, so verification re-renders the same
// shape from the manifest it read, which is why signing and verifying share
// this one function rather than each keeping a copy that could drift.
//
// events_file is last and omitted when empty, so a manifest written before the
// field existed renders exactly the bytes it was signed over and still
// verifies, while every manifest that names its rows signs that name.
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
