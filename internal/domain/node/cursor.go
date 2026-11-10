// Package node defines node values, list views, resolve records, node types,
// and the NodeReader interface for reads.
package node

import (
	"encoding/base64"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
)

// EncodeCursor returns an opaque cursor that resumes a paged list after
// lastID. Callers must treat the cursor as opaque.
//
// The cursor uses the bytes of lastID.
//
//   - The cursor identifies the last node rather than an offset in the list.
func EncodeCursor(lastID uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString(lastID[:])
}

// DecodeCursor returns the node ID after which a cursor resumes a paged list.
//
// The cursor must decode as a UUID. Invalid encoding or a decoded value with
// the wrong length returns an error that wraps domain.ErrInvalidArgument.
//
//   - A valid cursor returns the last node ID and a nil error.
func DecodeCursor(cursor string) (uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		slog.Warn("cursor.decode_failed", slog.String("cursor", cursor), slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("decode cursor %q: %w", cursor, domain.ErrInvalidArgument)
	}
	lastID, err := uuid.FromBytes(raw)
	if err != nil {
		slog.Warn("cursor.decode_failed", slog.String("cursor", cursor), slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("decode cursor %q: %w", cursor, domain.ErrInvalidArgument)
	}
	return lastID, nil
}
