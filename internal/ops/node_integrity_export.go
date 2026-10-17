package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
)

// repairExportMode grants read and write access only to the export file owner.
const repairExportMode = 0o600

// RepairExportReader reads a node's complete stored state for export.
type RepairExportReader interface {
	RepairExport(ctx context.Context, nodeID uuid.UUID) (fdbadapter.RepairExportRecord, error)
}

// repairExportDocument contains the records and repair details for one export.
type repairExportDocument struct {
	Ticket         string                        `json:"ticket"`
	RepairID       string                        `json:"repair_id"`
	ManifestDigest string                        `json:"manifest_digest"`
	NodeID         uuid.UUID                     `json:"node_id"`
	PrimaryDigest  string                        `json:"primary_digest"`
	ExportedAt     time.Time                     `json:"exported_at"`
	ExportedBy     uuid.UUID                     `json:"exported_by"`
	Resolve        json.RawMessage               `json:"resolve"`
	Primary        json.RawMessage               `json:"primary"`
	View           json.RawMessage               `json:"view"`
	Relationships  []fdbadapter.RepairExportEdge `json:"relationships"`
}

// exportRepairNode exports the node's complete stored state, reads the file
// back, and returns its SHA-256 digest. It returns an error wrapping
// [domain.ErrConflict] if the node is missing or its primary digest differs
// from the frozen source.
func exportRepairNode(ctx context.Context, reader RepairExportReader, entry NodeIntegrityEntry, manifestDigest string, actorID uuid.UUID) (string, error) {
	path := entry.Delete.ExportFile
	if !filepath.IsAbs(path) {
		return "", repairExportRefused(ctx, entry, fmt.Sprintf("export file %q is not an absolute path", path), domain.ErrInvalidArgument)
	}
	record, err := reader.RepairExport(ctx, entry.NodeID)
	if err != nil {
		return "", repairExportFailure(ctx, entry, err)
	}
	if !record.Found || primaryDigest(record.Primary) != entry.Source.PrimaryDigest {
		return "", repairExportRefused(ctx, entry, "the current primary record differs from the frozen source", domain.ErrConflict)
	}
	document := repairExportDocument{
		Ticket: nodeIntegrityTicket, RepairID: entry.RepairID, ManifestDigest: manifestDigest, NodeID: entry.NodeID,
		PrimaryDigest: entry.Source.PrimaryDigest, ExportedAt: clock.Now().UTC(), ExportedBy: actorID,
		Resolve: record.Resolve, Primary: record.Primary, View: record.View, Relationships: record.Relationships,
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return "", repairExportFailure(ctx, entry, err)
	}
	if err := writeRepairExport(ctx, entry, encoded); err != nil {
		return "", err
	}
	return verifyRepairExport(ctx, entry)
}

// writeRepairExport writes encoded to a temporary file with mode 0600 in
// the target directory, then renames the file to the export path.
func writeRepairExport(ctx context.Context, entry NodeIntegrityEntry, encoded []byte) error {
	path := entry.Delete.ExportFile
	file, err := os.CreateTemp(filepath.Dir(path), ".repair-export-*")
	if err != nil {
		return repairExportFailure(ctx, entry, err)
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if err := file.Chmod(repairExportMode); err != nil {
		_ = file.Close()
		return repairExportFailure(ctx, entry, err)
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return repairExportFailure(ctx, entry, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return repairExportFailure(ctx, entry, err)
	}
	if err := file.Close(); err != nil {
		return repairExportFailure(ctx, entry, err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return repairExportFailure(ctx, entry, err)
	}
	return nil
}

// verifyRepairExport checks the export's node ID, repair ID, and primary
// digest against entry and its frozen source. It returns the file's SHA-256
// digest.
func verifyRepairExport(ctx context.Context, entry NodeIntegrityEntry) (string, error) {
	encoded, err := os.ReadFile(entry.Delete.ExportFile)
	if err != nil {
		return "", repairExportFailure(ctx, entry, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var document repairExportDocument
	if err := decoder.Decode(&document); err != nil {
		return "", repairExportFailure(ctx, entry, err)
	}
	if document.NodeID != entry.NodeID || document.RepairID != entry.RepairID ||
		document.PrimaryDigest != entry.Source.PrimaryDigest || primaryDigest(document.Primary) != entry.Source.PrimaryDigest {
		return "", repairExportRefused(ctx, entry, fmt.Sprintf("export file %q does not record the frozen source", entry.Delete.ExportFile), domain.ErrConflict)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func repairExportRefused(ctx context.Context, entry NodeIntegrityEntry, reason string, sentinel error) error {
	wrapped := fmt.Errorf("export node %s for repair %s: %s: %w", entry.NodeID, entry.RepairID, reason, sentinel)
	slog.ErrorContext(ctx, "repair.export_refused", slog.String("err", wrapped.Error()),
		slog.String("repair_id", entry.RepairID), slog.String("node_id", entry.NodeID.String()))
	return wrapped
}

func repairExportFailure(ctx context.Context, entry NodeIntegrityEntry, err error) error {
	wrapped := fmt.Errorf("export node %s for repair %s: %w", entry.NodeID, entry.RepairID, err)
	slog.ErrorContext(ctx, "repair.export_failed", slog.String("err", wrapped.Error()),
		slog.String("repair_id", entry.RepairID), slog.String("node_id", entry.NodeID.String()))
	return wrapped
}
