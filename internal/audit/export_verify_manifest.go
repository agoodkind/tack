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

// maxManifestBytes bounds the memory used to read an untrusted manifest.
const maxManifestBytes = 1 << 20

// readExportManifest parses an untrusted manifest. Signature verification
// determines trust later; the scan must not allocate from claimed counts.
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
	// Reject unknown keys because the signature covers only declared fields.
	// The verifier must not silently discard an injected, unsigned field.
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

// manifestSignatureOK verifies the manifest with the export writer's shared
// signed-payload encoding. A malformed signature or missing or invalid key
// returns false rather than preventing the caller from completing its report.
func manifestSignatureOK(mf ExportManifest, pub ed25519.PublicKey) bool {
	manifestForVerify := exportSignableManifest(&mf)
	sig, err := hex.DecodeString(mf.Signature)
	// ed25519.Verify panics on invalid key lengths. Reject them before calling it.
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}

	return ed25519.Verify(pub, manifestForVerify, sig)
}
