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
	builder := newManifestBuilder(func(payload []byte) error {
		parts = append(parts, payload)
		return nil
	})
	for _, target := range targets {
		if err := builder.add(target); err != nil {
			return nil, err
		}
	}
	if err := builder.finish(); err != nil {
		return nil, err
	}
	return parts, nil
}

// manifestBuilder groups read targets in order into JSON arrays within
// ReadManifestMaxEntries and EventPartMaxBytes. It passes each finished array
// to emit and keeps at most one unfinished array.
type manifestBuilder struct {
	current      []Target
	currentBytes int
	emit         func(payload []byte) error
}

// newManifestBuilder returns an empty builder that passes finished arrays to
// emit.
func newManifestBuilder(emit func(payload []byte) error) *manifestBuilder {
	return &manifestBuilder{current: make([]Target, 0, ReadManifestMaxEntries), currentBytes: len("[]"), emit: emit}
}

// add appends target to the unfinished array. It first finishes that array
// when the array has ReadManifestMaxEntries targets or the target does not
// fit in EventPartMaxBytes. It rejects a target that cannot fit in one array.
func (b *manifestBuilder) add(target Target) error {
	encoded, err := json.Marshal(target)
	if err != nil {
		slog.Error("audit.read_manifest.target_encode_failed", slog.String("err", err.Error()))
		return fmt.Errorf("encode read target: %w", err)
	}
	targetBytes := len(encoded) + len(",")
	if len(encoded)+len("[]") > EventPartMaxBytes {
		return fmt.Errorf("read target of %d bytes exceeds the part size limit", len(encoded))
	}
	if len(b.current) == ReadManifestMaxEntries || b.currentBytes+targetBytes > EventPartMaxBytes {
		if err := b.flush(); err != nil {
			return err
		}
	}
	b.current = append(b.current, target)
	b.currentBytes += targetBytes
	return nil
}

// finish passes the unfinished array to emit when the array is not empty.
func (b *manifestBuilder) finish() error {
	if len(b.current) == 0 {
		return nil
	}
	return b.flush()
}

// flush encodes the unfinished array, starts a new one, and passes the encoded
// array to emit.
func (b *manifestBuilder) flush() error {
	payload, err := json.Marshal(b.current)
	if err != nil {
		slog.Error("audit.read_manifest.encode_failed", slog.String("err", err.Error()))
		return fmt.Errorf("encode read manifest part: %w", err)
	}
	b.current = b.current[:0]
	b.currentBytes = len("[]")
	return b.emit(payload)
}
