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

// maxManifestBytes limits manifest reads before verification. A typical
// exporter manifest contains a few hundred bytes of fixed metadata. Reading
// an entire foreign manifest before checking its size could exhaust the
// verifier's memory before rejection.
const maxManifestBytes = 1 << 20

// readExportManifest loads and parses the manifest without trusting its
// contents. Signature verification checks authenticity later. The scan does
// not size allocations from the manifest's claims.
func readExportManifest(dir string) (ExportManifest, error) {
	var mf ExportManifest
	path := filepath.Join(dir, exportManifestFile)
	file, err := os.Open(path)
	if err != nil {
		slog.Error(
			"audit.verify.manifest_read_failed",
			slog.String("dir", dir),
			slog.String("err", err.Error()),
		)

		return mf, fmt.Errorf("verify manifest: %w", err)
	}

	defer func() { _ = file.Close() }()
	mfBytes, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		slog.Error(
			"audit.verify.manifest_read_failed",
			slog.String("dir", dir),
			slog.String("err", err.Error()),
		)

		return mf, fmt.Errorf("verify manifest: %w", err)
	}

	if len(mfBytes) > maxManifestBytes {
		err := fmt.Errorf("verify manifest: %s exceeds %d bytes", path, maxManifestBytes)
		slog.Error(
			"audit.verify.manifest_oversized",
			slog.String("dir", dir),
			slog.String("err", err.Error()),
		)

		return mf, err
	}

	decoder := json.NewDecoder(bytes.NewReader(mfBytes))
	// The decoder rejects keys that ExportManifest does not declare. The
	// signature covers only declared fields. Ignoring an unknown key would
	// allow an unsigned field in a manifest that passes verification.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&mf); err != nil {
		slog.Error(
			"audit.verify.manifest_parse_failed",
			slog.String("dir", dir),
			slog.String("err", err.Error()),
		)

		return mf, fmt.Errorf("verify manifest parse: %w", err)
	}

	return mf, nil
}

// manifestSignatureOK reports whether the manifest signature verifies. It
// returns false for a malformed signature or a missing or incorrectly sized
// key. The caller can still return the rest of the verification report.
//
// Export and verification use exportSignableManifest to render signed bytes.
// Separate field lists could omit a field from signature protection,
// including the events filename, which must remain signed and unforgeable.
func manifestSignatureOK(mf ExportManifest, pub ed25519.PublicKey) bool {
	manifestForVerify := exportSignableManifest(&mf)
	sig, err := hex.DecodeString(mf.Signature)
	// ed25519.Verify panics when the public key has the wrong length. This
	// check rejects that key before verification.
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}

	return ed25519.Verify(pub, manifestForVerify, sig)
}
