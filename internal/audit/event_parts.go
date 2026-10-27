package audit

import (
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

// EncodedEvent contains the event ID, the transport digest, the manifest message
// in Header, and the ordered parts in Parts. The transport digest does not cover Header.
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
		return EncodedEvent{}, errors.New("encode audit event: event ID is missing")
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

// checkReadManifest rejects targets without read evidence and evidence
// that differs from ReadManifestDigest in digest or part count.
// It also accepts an empty evidence digest and zero part count when
// the target list is empty.
func checkReadManifest(event Event, targets []Target) error {
	read := event.Context.Read
	if read == nil {
		if len(targets) > 0 {
			return errors.New("has read targets without read evidence")
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
		return errors.New("has read evidence that does not match its read targets")
	}
	return nil
}

// sha256Hex returns the lowercase hexadecimal SHA-256 digest of payload.
func sha256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
