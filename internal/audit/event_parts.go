package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

const (
	// EventPartMaxBytes limits each event part payload and read manifest
	// JSON array to 64 KiB.
	EventPartMaxBytes = 64 << 10
	// ReadManifestMaxEntries limits each read manifest part to 256 targets.
	ReadManifestMaxEntries = 256
)

// EventPart contains one envelope segment, its zero-based index and total
// part count, and SHA-256 digests of its payload and the complete envelope.
type EventPart struct {
	EventID         uuid.UUID
	Index, Count    uint32
	Digest          string
	TransportDigest string
	Payload         []byte
}

// EncodedEvent stores ordered event parts and their manifest message in Header.
// The transport digest excludes Header, including commit_version.
type EncodedEvent struct {
	EventID         uuid.UUID
	TransportDigest string
	Header          []byte
	Parts           []EventPart
	single          bool `exhaustruct:"optional"`
}

// eventEnvelope pairs an audit Event with its ordered read targets under
// the JSON keys event and read_targets.
type eventEnvelope struct {
	Event       Event    `json:"event"`
	ReadTargets []Target `json:"read_targets"`
}

// EncodeEvent validates the event ID and read evidence, marshals the envelope,
// and splits its bytes into parts within EventPartMaxBytes.
// It computes the transport digest from the complete envelope.
func EncodeEvent(event Event, readTargets []Target) (EncodedEvent, error) {
	if event.EventID == uuid.Nil {
		return EncodedEvent{}, errors.New("encode audit event: event ID required")
	}
	if err := checkReadManifest(event, readTargets); err != nil {
		return EncodedEvent{}, fmt.Errorf("encode audit event %s: %s", event.EventID, err.Error())
	}
	targets := readTargets
	if targets == nil {
		targets = []Target{}
	}
	envelope, err := json.Marshal(eventEnvelope{Event: event, ReadTargets: targets})
	if err != nil {
		slog.Error("audit.event_parts.encode_failed", slog.String("event_id", event.EventID.String()), slog.String("err", err.Error()))
		return EncodedEvent{}, fmt.Errorf("encode audit event %s: %w", event.EventID, err)
	}
	transportDigest := sha256Hex(envelope)
	parts := make([]EventPart, 0, len(envelope)/EventPartMaxBytes+1)
	var count uint32
	for start := 0; start < len(envelope); start += EventPartMaxBytes {
		payload := envelope[start:min(start+EventPartMaxBytes, len(envelope))]
		parts = append(parts, EventPart{
			EventID: event.EventID, Index: count, Count: 0,
			Digest: sha256Hex(payload), TransportDigest: transportDigest, Payload: payload,
		})
		count++
	}
	for position := range parts {
		parts[position].Count = count
	}
	header, err := encodeTransportMessage(newManifestMessage(event, count, transportDigest))
	if err != nil {
		return EncodedEvent{}, err
	}
	return EncodedEvent{
		EventID: event.EventID, TransportDigest: transportDigest, Header: header, Parts: parts,
		single: count == 1 && len(targets) == 0,
	}, nil
}

// Multipart reports whether outbox and Kafka delivery requires part and
// manifest messages. It returns false only when the envelope fits in one
// part and the read target list is empty.
func (e EncodedEvent) Multipart() bool {
	return !e.single
}

// VerifyEventParts checks the manifest, part order and count, payload limits,
// and digests, then decodes the event and read targets.
// It rejects unknown JSON fields, event identity mismatches with the header,
// and read evidence that does not match the targets.
func VerifyEventParts(header []byte, parts []EventPart) (Event, []Target, error) {
	manifest, err := decodeManifestHeader(header)
	if err != nil {
		return Event{}, nil, err
	}
	envelope := make([]byte, 0, len(parts)*EventPartMaxBytes)
	var expected uint32
	for _, part := range parts {
		if reason := partMismatch(manifest, part, expected); reason != "" {
			return Event{}, nil, fmt.Errorf("verify audit event %s part %d: %s", manifest.EventID, part.Index, reason)
		}
		envelope = append(envelope, part.Payload...)
		expected++
	}
	if expected != manifest.PartCount {
		return Event{}, nil, fmt.Errorf("verify audit event %s: received %d of %d parts", manifest.EventID, expected, manifest.PartCount)
	}
	if sha256Hex(envelope) != manifest.TransportDigest {
		return Event{}, nil, fmt.Errorf("verify audit event %s: transport digest does not match the parts", manifest.EventID)
	}
	var decoded eventEnvelope
	decoder := json.NewDecoder(bytes.NewReader(envelope))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return Event{}, nil, fmt.Errorf("verify audit event %s: decode envelope: %s", manifest.EventID, err.Error())
	}
	event := decoded.Event
	if event.EventID != manifest.EventID || event.Context.OrgID != manifest.OrgID || shardOf(event.Actor.ID, event.EventID) != manifest.Shard {
		return Event{}, nil, fmt.Errorf("verify audit event %s: envelope identity does not match the header", manifest.EventID)
	}
	if err := checkReadManifest(event, decoded.ReadTargets); err != nil {
		return Event{}, nil, fmt.Errorf("verify audit event %s: %s", manifest.EventID, err.Error())
	}
	return event, decoded.ReadTargets, nil
}

// partMismatch returns the first reason a part fails validation against
// the manifest and expected index. It returns an empty string on success.
func partMismatch(manifest transportMessage, part EventPart, expected uint32) string {
	switch {
	case part.EventID != manifest.EventID:
		return "event ID differs from the header"
	case part.Index != expected:
		return fmt.Sprintf("part is out of order, want index %d", expected)
	case part.Count != manifest.PartCount || part.TransportDigest != manifest.TransportDigest:
		return "part count or transport digest differs from the header"
	case len(part.Payload) > EventPartMaxBytes:
		return "payload exceeds the part limit"
	case sha256Hex(part.Payload) != part.Digest:
		return "payload does not match the part digest"
	default:
		return ""
	}
}

// checkReadManifest rejects targets without read evidence and evidence
// that differs from ReadManifestDigest in digest or part count.
// It also accepts an empty evidence digest and zero part count when
// the target list is empty.
func checkReadManifest(event Event, targets []Target) error {
	read := event.Context.Read
	if read == nil {
		if len(targets) > 0 {
			return errors.New("read targets require read evidence")
		}
		return nil
	}
	if len(targets) == 0 && read.ManifestSHA256 == "" && read.ManifestParts == 0 {
		return nil
	}
	digest, partCount, err := ReadManifestDigest(targets)
	if err != nil {
		return err
	}
	if digest != read.ManifestSHA256 || partCount != read.ManifestParts {
		return errors.New("read evidence manifest does not match the read targets")
	}
	return nil
}

// sha256Hex returns the lowercase hexadecimal SHA-256 digest of payload.
func sha256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
