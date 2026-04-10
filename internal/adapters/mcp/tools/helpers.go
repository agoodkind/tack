package tools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func parseUUID(s, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s: %w", field, err)
	}
	return id, nil
}

func parseOptionalUUID(s string) *uuid.UUID {
	if s == "" {
		return nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil
	}
	return &id
}

// parseOptionalDate parses an optional date string (YYYY-MM-DD) into a *time.Time.
// Returns nil if the string is empty; returns an error for invalid formats.
func parseOptionalDate(s, field string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, fmt.Errorf("invalid %s (expected YYYY-MM-DD): %w", field, err)
	}
	return &t, nil
}

// mustUser extracts the authenticated user ID from context.
// Returns an error suitable for returning directly from a tool handler.
func mustUser(ctx context.Context) (uuid.UUID, error) {
	id, ok := auth.UserID(ctx)
	if !ok {
		return uuid.Nil, errors.New("unauthenticated")
	}
	return id, nil
}

func errNotFound(entity string) error {
	return fmt.Errorf("%s: %w", entity, domain.ErrNotFound)
}

func errNotImplemented(msg string) error {
	return fmt.Errorf("not implemented: %s", msg)
}

// toolError wraps a domain error into a user-facing MCP error string.
func toolError(_ *mcp.CallToolRequest, err error) string {
	if errors.Is(err, domain.ErrNotFound) {
		return "not found"
	}
	if errors.Is(err, domain.ErrUnauthenticated) {
		return "unauthenticated"
	}
	return err.Error()
}
