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
	// searchCursorFormat versions the cursor payload layout.
	searchCursorFormat = 1
	// searchCursorPayloadBytes is the byte length of the format byte, the
	// session ID, and the version.
	searchCursorPayloadBytes = 1 + 16 + 8
)

// errInvalidSearchCursor means the cursor is malformed or fails authentication.
var errInvalidSearchCursor = errors.New("search cursor is invalid")

// SearchCursorCodec authenticates continuation cursors with HMAC-SHA256.
// Every Tack process uses the same configured key.
type SearchCursorCodec struct {
	key []byte
}

// NewSearchCursorCodec copies the configured cursor key.
func NewSearchCursorCodec(key []byte) *SearchCursorCodec {
	return &SearchCursorCodec{key: append([]byte(nil), key...)}
}

// Encode returns the opaque cursor for one session version.
func (c *SearchCursorCodec) Encode(sessionID uuid.UUID, version uint64) string {
	payload := make([]byte, 0, searchCursorPayloadBytes+sha256.Size)
	payload = append(payload, searchCursorFormat)
	payload = append(payload, sessionID[:]...)
	payload = binary.BigEndian.AppendUint64(payload, version)
	return base64.RawURLEncoding.EncodeToString(append(payload, c.mac(payload)...))
}

// Decode authenticates the cursor and returns its session ID and version.
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
