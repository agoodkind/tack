package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

const (
	// eventPartsFormatVersion sets the supported part and manifest format to 1.
	eventPartsFormatVersion uint16 = 1
	// messageKindPart identifies an event part with the audit_message value
	// event_part.
	messageKindPart = "event_part"
	// messageKindManifest identifies a manifest with the audit_message value
	// event_manifest.
	messageKindManifest = "event_manifest"
)

// transportMessage contains the JSON fields for a part or manifest message.
type transportMessage struct {
	Message         string    `json:"audit_message"`
	FormatVersion   uint16    `json:"format_version"`
	EventID         uuid.UUID `json:"event_id"`
	OrgID           uuid.UUID `json:"org_id"`
	Shard           int16     `json:"shard"`
	PartCount       uint32    `json:"part_count"`
	TransportDigest string    `json:"transport_digest"`
	PartIndex       uint32    `json:"part_index,omitzero"`
	PartDigest      string    `json:"part_digest,omitempty"`
	Payload         []byte    `json:"payload,omitempty"`
	CommitVersion   string    `json:"commit_version,omitempty"`
}

// messageProbe contains only audit_message for the initial transport check.
type messageProbe struct {
	Message string `json:"audit_message"`
}

// newManifestMessage creates a manifest with the event ID, organization ID,
// shard, part count, and transport digest.
func newManifestMessage(event Event, count uint32, transportDigest string) transportMessage {
	return transportMessage{
		Message: messageKindManifest, FormatVersion: eventPartsFormatVersion,
		EventID: event.EventID, OrgID: event.Context.OrgID, Shard: shardOf(event.Actor.ID, event.EventID),
		PartCount: count, TransportDigest: transportDigest,
		PartIndex: 0, PartDigest: "", Payload: nil, CommitVersion: "",
	}
}

// encodeTransportMessage marshals a transport message to JSON and reports
// encoding errors with the event ID.
func encodeTransportMessage(message transportMessage) ([]byte, error) {
	encoded, err := json.Marshal(message)
	if err != nil {
		slog.Error("audit.event_parts.message_encode_failed", slog.String("event_id", message.EventID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("encode audit transport message for event %s: %w", message.EventID, err)
	}
	return encoded, nil
}

// Messages returns JSON part messages in Parts order, followed by Header.
// The relay publishes the messages in that order.
func (e EncodedEvent) Messages() ([][]byte, error) {
	manifest, err := decodeManifestHeader(e.Header)
	if err != nil {
		return nil, err
	}
	messages := make([][]byte, 0, len(e.Parts)+1)
	for _, part := range e.Parts {
		message := manifest
		message.Message = messageKindPart
		message.PartIndex, message.PartDigest, message.Payload = part.Index, part.Digest, part.Payload
		encoded, err := encodeTransportMessage(message)
		if err != nil {
			return nil, err
		}
		messages = append(messages, encoded)
	}
	return append(messages, e.Header), nil
}

// Records returns outbox records in publication order.
// When Multipart returns false, it returns plain event JSON as one record.
// Otherwise, it returns ordered part messages followed by the manifest message.
func (e EncodedEvent) Records() ([][]byte, error) {
	if e.Multipart() {
		return e.Messages()
	}
	if len(e.Parts) != 1 {
		return nil, fmt.Errorf("audit event %s: a single record requires exactly one part", e.EventID)
	}
	var envelope eventEnvelope
	decoder := json.NewDecoder(bytes.NewReader(e.Parts[0].Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("audit event %s: decode envelope: %s", e.EventID, err.Error())
	}
	eventJSON, err := MarshalEvent(envelope.Event)
	if err != nil {
		return nil, err
	}
	return [][]byte{eventJSON}, nil
}

// FitsSingleRecord reports whether eventJSON fits in one plain outbox record
// at or below the EventPartMaxBytes size limit.
func FitsSingleRecord(eventJSON []byte) bool {
	return len(eventJSON) <= EventPartMaxBytes
}

// decodeTransportMessage detects transport messages and rejects unknown fields
// during decoding. It returns false without an error if the probe fails
// or audit_message is empty. It requires a supported version and kind,
// a nonzero event ID and part count, and a valid transport digest.
func decodeTransportMessage(raw []byte) (transportMessage, bool, error) {
	var message transportMessage
	var probe messageProbe
	probeErr := json.Unmarshal(raw, &probe)
	isTransport := probeErr == nil && probe.Message != ""
	if !isTransport {
		return message, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&message); err != nil {
		return transportMessage{}, true, fmt.Errorf("decode audit transport message: %s", err.Error())
	}
	if message.FormatVersion != eventPartsFormatVersion {
		return transportMessage{}, true, fmt.Errorf("audit transport message %s uses unsupported format version %d", message.EventID, message.FormatVersion)
	}
	if message.Message != messageKindPart && message.Message != messageKindManifest {
		return transportMessage{}, true, fmt.Errorf("audit transport message %s has unsupported kind %q", message.EventID, message.Message)
	}
	if message.EventID == uuid.Nil || message.PartCount == 0 || !isSHA256Hex(message.TransportDigest) {
		return transportMessage{}, true, fmt.Errorf("audit transport message %s has an invalid event ID, part count, or transport digest", message.EventID)
	}
	return message, true, nil
}

// decodeManifestHeader decodes and validates Header as a manifest message.
func decodeManifestHeader(header []byte) (transportMessage, error) {
	message, isTransport, err := decodeTransportMessage(header)
	if err != nil {
		return transportMessage{}, err
	}
	if !isTransport || message.Message != messageKindManifest {
		return transportMessage{}, errors.New("audit event header is not a manifest message")
	}
	return message, nil
}
