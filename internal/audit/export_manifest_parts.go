package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"log/slog"

	"github.com/google/uuid"
)

// errManifestPartSource reports that an export row source cannot provide
// read manifest parts.
var errManifestPartSource = errors.New(
	"audit export: the row source does not provide read manifest parts",
)

// ManifestPart represents one permanent read manifest part in an export row.
// It contains the part index, a JSON array of read targets, and the SHA-256
// digest of that payload.
type ManifestPart struct {
	Index   uint32 `json:"index"`
	Digest  string `json:"digest"`
	Payload []byte `json:"payload"`
}

// manifestPartSource lets an export row source provide read manifest parts.
type manifestPartSource interface {
	ManifestParts(ctx context.Context, orgID, eventID uuid.UUID) ([]ManifestPart, error)
}

// ManifestParts reads one event's read manifest parts for one organization
// in index order.
func (r *Reader) ManifestParts(
	ctx context.Context,
	orgID uuid.UUID,
	eventID uuid.UUID,
) ([]ManifestPart, error) {
	rows, err := r.pool.Rows(
		r.pool.unsanitized.Query(
			ctx,
			`
		SELECT part_index, digest, payload FROM audit.event_parts
		 WHERE org_id = $1 AND event_id = $2
		 ORDER BY part_index ASC
	`,
			orgID,
			eventID,
		),
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.reader.manifest_query_failed",
			slog.String("event_id", eventID.String()),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("read audit event %s manifest parts: %w", eventID, err)
	}
	defer rows.Close()
	parts := make([]ManifestPart, 0)

	for rows.Next() {
		part := ManifestPart{Index: 0, Digest: "", Payload: nil}
		if err := rows.Scan(&part.Index, &part.Digest, &part.Payload); err != nil {
			slog.ErrorContext(
				ctx,
				"audit.reader.manifest_scan_failed",
				slog.String("event_id", eventID.String()),
				slog.String("err", err.Error()),
			)
			return nil, fmt.Errorf("scan audit event %s manifest parts: %w", eventID, err)
		}

		parts = append(parts, part)
	}

	if err := rows.Err(); err != nil {
		slog.ErrorContext(
			ctx,
			"audit.reader.manifest_scan_failed",
			slog.String("event_id", eventID.String()),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("read audit event %s manifest part rows: %w", eventID, err)
	}

	return parts, nil
}

// attachManifestParts adds read manifest parts to an export row that declares
// them. It requires the row source to implement manifestPartSource.
func attachManifestParts(ctx context.Context, source RowSource, row *Row) error {
	if row.Context.Read == nil || row.Context.Read.ManifestParts == 0 {
		return nil
	}

	parts, ok := source.(manifestPartSource)
	if !ok {
		return errManifestPartSource
	}

	manifest, err := parts.ManifestParts(ctx, row.OrgID, row.EventID)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.export.manifest_read_failed",
			slog.String("event_id", row.EventID.String()),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("export audit event %s manifest parts: %w", row.EventID, err)
	}

	row.ManifestParts = manifest

	return nil
}

// manifestViolation returns the first read manifest failure or an empty
// string when its checks pass. It checks evidence, part order, count,
// payloads, and digests. The row hash covers the manifest digest and part
// count in the context; the payload digest check authenticates that manifest root.
func manifestViolation(row Row) string {
	read := row.Context.Read
	if read == nil || (read.ManifestParts == 0 && read.ManifestSHA256 == "") {
		if len(row.ManifestParts) > 0 {
			return "does not declare read manifest evidence for its parts"
		}

		return ""
	}

	validator := newManifestValidator()

	for _, part := range row.ManifestParts {
		if violation := validator.add(part, false); violation != "" {
			return violation
		}
	}

	return validator.finish(read, false)
}

type manifestValidator struct {
	root       hash.Hash
	parts      uint32
	identities uint64
}

func newManifestValidator() *manifestValidator {
	return &manifestValidator{root: sha256.New(), parts: 0, identities: 0}
}

func (v *manifestValidator) add(part ManifestPart, validateIdentities bool) string {
	if part.Index != v.parts {
		return fmt.Sprintf("does not provide read manifest part %d in index order", v.parts)
	}

	if len(part.Payload) > EventPartMaxBytes || SHA256Hex(part.Payload) != part.Digest {
		return fmt.Sprintf(
			"has read manifest part %d with a payload that exceeds the size limit or does not match its digest",
			part.Index,
		)
	}

	count := manifestPayloadCount(part.Payload, validateIdentities)
	if count == 0 {
		return fmt.Sprintf(
			"has read manifest part %d that does not decode as a valid list of read targets",
			part.Index,
		)
	}

	_, _ = v.root.Write(part.Payload)
	v.parts++
	v.identities += count

	return ""
}

func (v *manifestValidator) finish(read *ReadEvidence, checkCount bool) string {
	if v.parts != read.ManifestParts {
		return fmt.Sprintf("has %d of %d read manifest parts", v.parts, read.ManifestParts)
	}

	if hex.EncodeToString(v.root.Sum(nil)) != read.ManifestSHA256 {
		return "has read manifest parts that do not match the manifest digest"
	}

	if checkCount && v.identities != read.Count {
		return fmt.Sprintf("has %d of %d declared read identities", v.identities, read.Count)
	}

	return ""
}

// manifestPayloadCount counts a valid, nonempty list of read targets.
func manifestPayloadCount(payload []byte, validateIdentities bool) uint64 {
	var targets []Target
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&targets) != nil || len(targets) == 0 ||
		len(targets) > ReadManifestMaxEntries {
		return 0
	}

	if !decoderAtEnd(decoder) {
		return 0
	}

	var count uint64

	for _, target := range targets {
		if validateIdentities && validateTarget(target) != nil {
			return 0
		}

		count++
	}

	return count
}
