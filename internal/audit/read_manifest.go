package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
)

// ReadManifestDigest returns the lowercase hexadecimal SHA-256 digest of
// the manifest array bytes concatenated in order, plus the array count.
// An empty target list produces the digest of zero bytes and a count of zero.
func ReadManifestDigest(targets []Target) (string, uint32, error) {
	parts, err := readManifestParts(targets)
	if err != nil {
		return "", 0, err
	}
	hasher := sha256.New()
	var count uint32
	for _, part := range parts {
		_, _ = hasher.Write(part)
		count++
	}
	return hex.EncodeToString(hasher.Sum(nil)), count, nil
}

// readManifestParts encodes targets in order as JSON arrays within
// ReadManifestMaxEntries and EventPartMaxBytes.
// It rejects a target that cannot fit in one array.
func readManifestParts(targets []Target) ([][]byte, error) {
	parts := make([][]byte, 0, len(targets)/ReadManifestMaxEntries+1)
	current := make([]Target, 0, min(len(targets), ReadManifestMaxEntries))
	currentBytes := len("[]")
	flush := func() error {
		payload, err := json.Marshal(current)
		if err != nil {
			slog.Error("audit.read_manifest.encode_failed", slog.String("err", err.Error()))
			return fmt.Errorf("encode read manifest part: %w", err)
		}
		parts = append(parts, payload)
		current = current[:0]
		currentBytes = len("[]")
		return nil
	}
	for _, target := range targets {
		encoded, err := json.Marshal(target)
		if err != nil {
			slog.Error("audit.read_manifest.target_encode_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("encode read target: %w", err)
		}
		targetBytes := len(encoded) + len(",")
		if len(encoded)+len("[]") > EventPartMaxBytes {
			return nil, fmt.Errorf("read target of %d bytes exceeds the part size limit", len(encoded))
		}
		if len(current) == ReadManifestMaxEntries || currentBytes+targetBytes > EventPartMaxBytes {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		current = append(current, target)
		currentBytes += targetBytes
	}
	if len(current) > 0 {
		if err := flush(); err != nil {
			return nil, err
		}
	}
	return parts, nil
}
