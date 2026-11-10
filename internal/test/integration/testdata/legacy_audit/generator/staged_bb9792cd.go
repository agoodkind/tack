package main

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// stagedIntents does not return payloads. Revision bb9792cd writes FoundationDB
// changes as format 2 change events rather than staged format 0 intents.
func stagedIntents(context.Context, uuid.UUID, uuid.UUID) ([]json.RawMessage, error) {
	return nil, nil
}
