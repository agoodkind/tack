package integration_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

const (
	partRestoreEventPartKind = `"event_part"`
	partRestoreChunkBytes    = 40000
)

type changeEncoding struct {
	header json.RawMessage
	parts  []json.RawMessage
}

func editRaw(t *testing.T, raw json.RawMessage, path []string, value json.RawMessage) json.RawMessage {
	t.Helper()
	if len(path) == 0 {
		return value
	}
	if index, err := strconv.Atoi(path[0]); err == nil {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Fatalf("decode the array at %v: %v", path, err)
		}
		items[index] = editRaw(t, items[index], path[1:], value)
		return marshalRaw(t, items)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		t.Fatalf("decode the object at %v: %v", path, err)
	}
	if len(path) == 1 && value == nil {
		delete(members, path[0])
	} else {
		members[path[0]] = editRaw(t, members[path[0]], path[1:], value)
	}
	return marshalRaw(t, members)
}

func marshalRaw[T []json.RawMessage | map[string]json.RawMessage | string](t *testing.T, value T) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode the edited JSON: %v", err)
	}
	return encoded
}

func quotedJSON(t *testing.T, text string) json.RawMessage {
	t.Helper()
	return marshalRaw(t, text)
}

func (e *changeEncoding) editHeader(t *testing.T, path string, value json.RawMessage) {
	t.Helper()
	e.header = editRaw(t, e.header, strings.Split(path, "."), value)
}

func (e *changeEncoding) editPart(t *testing.T, index int, path string, value json.RawMessage) {
	t.Helper()
	e.parts[index] = editRaw(t, e.parts[index], strings.Split(path, "."), value)
}

func (e *changeEncoding) editEveryPart(t *testing.T, path string, value json.RawMessage) {
	t.Helper()
	for index := range e.parts {
		e.editPart(t, index, path, value)
	}
}

func (e *changeEncoding) headerBytes() []byte {
	return e.header
}

func (e *changeEncoding) partBytes() [][]byte {
	parts := [][]byte{}
	for _, part := range e.parts {
		parts = append(parts, part)
	}
	return parts
}

func (e *changeEncoding) envelope(t *testing.T) []byte {
	t.Helper()
	var envelope []byte
	for _, part := range e.parts {
		var message struct {
			Payload []byte `json:"payload"`
		}
		if err := json.Unmarshal(part, &message); err != nil {
			t.Fatalf("decode a change part: %v", err)
		}
		envelope = append(envelope, message.Payload...)
	}
	return envelope
}

func (e *changeEncoding) repackage(t *testing.T, envelope []byte, chunk int) {
	t.Helper()
	digest := sha256.Sum256(envelope)
	digestText := hex.EncodeToString(digest[:])
	pieces := [][]byte{}
	for start := 0; start < len(envelope); start += chunk {
		pieces = append(pieces, envelope[start:min(start+chunk, len(envelope))])
	}
	template := editRaw(t, e.header, []string{"transport_digest"}, quotedJSON(t, digestText))
	template = editRaw(t, template, []string{"part_count"}, json.RawMessage(strconv.Itoa(len(pieces))))
	e.header = template
	e.parts = nil
	for index, piece := range pieces {
		pieceDigest := sha256.Sum256(piece)
		part := editRaw(t, template, []string{"audit_message"}, json.RawMessage(partRestoreEventPartKind))
		part = editRaw(t, part, []string{"part_digest"}, quotedJSON(t, hex.EncodeToString(pieceDigest[:])))
		part = editRaw(t, part, []string{"payload"}, quotedJSON(t, base64.StdEncoding.EncodeToString(piece)))
		if index > 0 {
			part = editRaw(t, part, []string{"part_index"}, json.RawMessage(strconv.Itoa(index)))
		}
		e.parts = append(e.parts, part)
	}
}
