package audit

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// maxManifestBytes bounds the manifest read. A real manifest is a few hundred
// bytes of fixed fields; anything larger is not one this exporter wrote, and
// reading it whole before checking anything would let a foreign bundle spend
// the verifier's memory on the line before it was rejected.
const maxManifestBytes = 1 << 20

// readExportManifest loads and parses the bundle manifest. Nothing in it is
// trusted yet: the signature check decides that, and the scan sizes nothing
// from what the manifest claims.
func readExportManifest(dir string) (ExportManifest, error) {
	var mf ExportManifest
	path := filepath.Join(dir, exportManifestFile)
	file, err := os.Open(path)
	if err != nil {
		slog.Error("audit.verify.manifest_read_failed", slog.String("dir", dir), slog.String("err", err.Error()))
		return mf, fmt.Errorf("verify manifest: %w", err)
	}
	defer func() { _ = file.Close() }()
	mfBytes, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		slog.Error("audit.verify.manifest_read_failed", slog.String("dir", dir), slog.String("err", err.Error()))
		return mf, fmt.Errorf("verify manifest: %w", err)
	}
	if len(mfBytes) > maxManifestBytes {
		err := fmt.Errorf("verify manifest: %s exceeds %d bytes", path, maxManifestBytes)
		slog.Error("audit.verify.manifest_oversized", slog.String("dir", dir), slog.String("err", err.Error()))
		return mf, err
	}
	decoder := json.NewDecoder(bytes.NewReader(mfBytes))
	// A manifest key the type does not declare is rejected, not dropped: the
	// signature covers only the declared fields, so a dropped key would be a
	// planted field that verifies.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&mf); err != nil {
		slog.Error("audit.verify.manifest_parse_failed", slog.String("dir", dir), slog.String("err", err.Error()))
		return mf, fmt.Errorf("verify manifest parse: %w", err)
	}
	return mf, nil
}

// manifestSignatureOK reports whether the manifest's signature verifies. A
// malformed signature, a missing key, or a key of the wrong size is a failed
// check, not a hard error, so the rest of the report still reaches the caller.
//
// The signed bytes are rendered by the same function the export signs, so the
// set of fields the signature covers cannot drift between the two sides. Adding
// a field to one and not the other would leave that field unsigned and
// forgeable, which is exactly what the events file name must never be.
func manifestSignatureOK(mf ExportManifest, pub ed25519.PublicKey) bool {
	manifestForVerify := exportSignableManifest(&mf)
	sig, err := hex.DecodeString(mf.Signature)
	// ed25519.Verify panics on a key of the wrong length rather than
	// returning false, so a malformed key has to be refused here.
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(pub, manifestForVerify, sig)
}
