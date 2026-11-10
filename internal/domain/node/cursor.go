// Package node defines the generic node model: node values, list views,
// resolve records, node types, and the NodeReader read path.
package node

import (
	"encoding/base64"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
)

// EncodeCursor returns the opaque cursor that resumes a paged list after
// lastID. Callers must treat the value as opaque.
func EncodeCursor(lastID uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString(lastID[:])
}

// DecodeCursor returns the node ID a cursor resumes after.
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
