package tools

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"

	"github.com/google/uuid"
)

const (
	// searchCursorFormat identifies the cursor payload layout version.
	searchCursorFormat = 1
	// searchCursorPayloadBytes is the byte length of the format byte, the
	// session ID, and the version.
	searchCursorPayloadBytes = 1 + 16 + 8
)

// errInvalidSearchCursor reports a malformed cursor or failed
// authentication.
var errInvalidSearchCursor = errors.New("search cursor is invalid")

// SearchCursorCodec authenticates continuation cursors with HMAC-SHA256,
// a keyed hash. Tack processes must use the same configured cursor key to
// accept each other's cursors.
type SearchCursorCodec struct {
	key []byte
}

// NewSearchCursorCodec copies the configured cursor key.
//
// The codec stores its own byte slice. Later changes to the input slice do
// not change the key that Encode and Decode use.
//
// With a nonnil sessionID, Decode accepts the cursor after an input change:
//
//	cursorKey := []byte("example cursor key")
//	codec := NewSearchCursorCodec(cursorKey)
//	cursor := codec.Encode(sessionID, 1)
//	cursorKey[0] ^= 0xff
//	_, _, err := codec.Decode(cursor)
func NewSearchCursorCodec(key []byte) *SearchCursorCodec {
	return &SearchCursorCodec{key: append([]byte(nil), key...)}
}

// Encode returns an opaque cursor for one session version.
//
// The cursor contains the format version, session ID, and session version.
// The codec appends a keyed hash and encodes the bytes as URL-safe base64.
//
// A search page supplies the session and next version:
//
//	cursor := codec.Encode(page.SessionID, page.NextVersion)
func (c *SearchCursorCodec) Encode(sessionID uuid.UUID, version uint64) string {
	payload := make([]byte, 0, searchCursorPayloadBytes+sha256.Size)
	payload = append(payload, searchCursorFormat)
	payload = append(payload, sessionID[:]...)
	payload = binary.BigEndian.AppendUint64(payload, version)
	return base64.RawURLEncoding.EncodeToString(append(payload, c.mac(payload)...))
}

// Decode authenticates a cursor and returns its session ID and version.
//
// The codec rejects malformed base64, an unexpected size or format, an
// invalid keyed hash, and a nil session ID with errInvalidSearchCursor.
//
// The search handler decodes a continuation before running the search:
//
//	sessionID, version, err := codec.Decode(cursor)
func (c *SearchCursorCodec) Decode(cursor string) (uuid.UUID, uint64, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(decoded) != searchCursorPayloadBytes+sha256.Size || decoded[0] != searchCursorFormat {
		return uuid.Nil, 0, errInvalidSearchCursor
	}
	payload := decoded[:searchCursorPayloadBytes]
	if !hmac.Equal(decoded[searchCursorPayloadBytes:], c.mac(payload)) {
		return uuid.Nil, 0, errInvalidSearchCursor
	}
	sessionID, err := uuid.FromBytes(payload[1:17])
	if err != nil || sessionID == uuid.Nil {
		return uuid.Nil, 0, errInvalidSearchCursor
	}
	return sessionID, binary.BigEndian.Uint64(payload[17:]), nil
}

func (c *SearchCursorCodec) mac(payload []byte) []byte {
	signer := hmac.New(sha256.New, c.key)
	_, _ = signer.Write(payload)
	return signer.Sum(nil)
}
