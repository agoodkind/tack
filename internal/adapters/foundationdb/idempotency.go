package foundationdb

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func decodeIdempotencyRecord(key string, raw []byte) (*node.IdempotencyRecord, error) {
	if id, err := uuid.FromBytes(raw); err == nil {
		return &node.IdempotencyRecord{Key: key, NodeID: id}, nil
	}
	var record node.IdempotencyRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("decode idempotency record: %w", err)
	}
	record.Key = key
	return &record, nil
}
