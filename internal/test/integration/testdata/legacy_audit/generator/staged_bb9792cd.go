package main

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// stagedIntents returns no payloads: bb9792cd writes FoundationDB changes as
// format 2 change events, not staged format 0 intents.
func stagedIntents(context.Context, uuid.UUID, uuid.UUID) ([]json.RawMessage, error) {
	return nil, nil
}
