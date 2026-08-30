package audit

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// readExportManifest loads and parses the bundle manifest. Nothing in it is
// trusted yet: the signature check decides that, and the scan sizes nothing
// from what the manifest claims.
func readExportManifest(dir string) (ExportManifest, error) {
	var mf ExportManifest
	mfBytes, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		slog.Error("audit.verify.manifest_read_failed", slog.String("dir", dir), slog.String("err", err.Error()))
		return mf, fmt.Errorf("verify manifest: %w", err)
	}
	if err := json.Unmarshal(mfBytes, &mf); err != nil {
		slog.Error("audit.verify.manifest_parse_failed", slog.String("dir", dir), slog.String("err", err.Error()))
		return mf, fmt.Errorf("verify manifest parse: %w", err)
	}
	return mf, nil
}

// manifestSignatureOK reports whether the manifest's signature verifies. A
// malformed signature or a missing key is a failed check, not a hard error,
// so the rest of the report still reaches the caller.
func manifestSignatureOK(dir string, mf ExportManifest, pub ed25519.PublicKey) bool {
	manifestForVerify, err := json.Marshal(struct {
		ExportID    uuid.UUID `json:"export_id"`
		OrgID       uuid.UUID `json:"org_id"`
		Oldest      time.Time `json:"oldest"`
		Latest      time.Time `json:"latest"`
		RowCount    int       `json:"row_count"`
		FileSHA256  string    `json:"file_sha256"`
		SignatureBy string    `json:"signing_key_id"`
	}{
		ExportID:    mf.ExportID,
		OrgID:       mf.OrgID,
		Oldest:      mf.Oldest,
		Latest:      mf.Latest,
		RowCount:    mf.RowCount,
		FileSHA256:  mf.FileSHA256,
		SignatureBy: mf.SignatureBy,
	})
	if err != nil {
		slog.Error("audit.verify.manifest_marshal_failed", slog.String("dir", dir), slog.String("err", err.Error()))
		return false
	}
	sig, err := hex.DecodeString(mf.Signature)
	if err != nil || pub == nil {
		return false
	}
	return ed25519.Verify(pub, manifestForVerify, sig)
}
