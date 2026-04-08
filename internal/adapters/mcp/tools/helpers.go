package tools

import (
	"errors"
	"fmt"

	"github.com/agoodkind/tack/internal/auth"
	"github.com/agoodkind/tack/internal/domain"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"context"
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
