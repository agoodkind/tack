package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// errAssemblyPartsInvalid marks a stored part that is missing or that fails
// its size or digest check.
var errAssemblyPartsInvalid = errors.New("assembly part check failed")

// assemblyPartReader returns the stored part payloads of one assembly in index
// order as one byte stream. It reads one part per fetch call and keeps only
// that part. It checks the size and digest of each part and adds the part
// bytes to the transport digest.
type assemblyPartReader struct {
	manifest transportMessage
	// fetch reads the digest and payload of one stored part and reports
	// whether the part exists.
	fetch   func(index uint32) (string, []byte, bool, error)
	next    uint32
	pending []byte
	hasher  hash.Hash
	// failure is the first error that Read returned other than [io.EOF].
	failure error
}

// newAssemblyPartReader returns a reader for the parts that manifest declares.
// It reads each part with one query in tx.
func newAssemblyPartReader(ctx context.Context, tx pgx.Tx, manifest transportMessage) *assemblyPartReader {
	fetch := func(index uint32) (string, []byte, bool, error) {
		var digest string
		var payload []byte
		err := tx.QueryRow(ctx, `
			SELECT digest, payload FROM audit.event_assembly_parts
			 WHERE event_id = $1 AND part_index = $2
		`, manifest.EventID, int64(index)).Scan(&digest, &payload)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, false, nil
		}
		if err != nil {
			slog.ErrorContext(ctx, "audit.consumer.parts_read_failed", slog.String("event_id", manifest.EventID.String()), slog.String("err", err.Error()))
			return "", nil, false, fmt.Errorf("read audit event %s part %d: %w", manifest.EventID, index, err)
		}
		return digest, payload, true, nil
	}
	return &assemblyPartReader{manifest: manifest, fetch: fetch, next: 0, pending: nil, hasher: sha256.New(), failure: nil}
}

// Read copies bytes of the current part into buffer. It reads the next part
// when the current part has no bytes left and returns [io.EOF] after the last
// declared part.
func (r *assemblyPartReader) Read(buffer []byte) (int, error) {
	for len(r.pending) == 0 {
		if r.failure != nil {
			return 0, r.failure
		}
		if r.next == r.manifest.PartCount {
			return 0, io.EOF
		}
		if err := r.load(); err != nil {
			r.failure = err
			return 0, err
		}
	}
	copied := copy(buffer, r.pending)
	r.pending = r.pending[copied:]
	return copied, nil
}

// load reads part r.next, checks it, and adds its bytes to the transport
// digest. A missing or invalid part returns errAssemblyPartsInvalid.
func (r *assemblyPartReader) load() error {
	digest, payload, found, err := r.fetch(r.next)
	if err != nil {
		return err
	}
	if !found {
		return r.invalidPart(fmt.Sprintf("received %d of %d parts", r.next, r.manifest.PartCount))
	}
	if len(payload) > EventPartMaxBytes || sha256Hex(payload) != digest {
		return r.invalidPart(fmt.Sprintf("part %d has an invalid size or does not match its digest", r.next))
	}
	_, _ = r.hasher.Write(payload)
	r.pending = payload
	r.next++
	return nil
}

// invalidPart logs reason and returns it wrapped with errAssemblyPartsInvalid.
func (r *assemblyPartReader) invalidPart(reason string) error {
	slog.Warn("audit.consumer.assembly_part_invalid", slog.String("event_id", r.manifest.EventID.String()), slog.String("reason", reason))
	return fmt.Errorf("%w: %s", errAssemblyPartsInvalid, reason)
}

// complete reports whether the reader returned every declared part and the
// part bytes match the transport digest of the manifest.
func (r *assemblyPartReader) complete() bool {
	return r.next == r.manifest.PartCount && hex.EncodeToString(r.hasher.Sum(nil)) == r.manifest.TransportDigest
}
