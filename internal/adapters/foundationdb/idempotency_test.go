package foundationdb

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func TestDecodeIdempotencyRecordLegacyUUID(t *testing.T) {
	nodeID := uuid.New()
	raw, _ := nodeID.MarshalBinary()

	record, err := decodeIdempotencyRecord("legacy", raw)
	if err != nil {
		t.Fatalf("decode legacy: %v", err)
	}
	if record.Key != "legacy" || record.NodeID != nodeID || record.Fingerprint != "" {
		t.Fatalf("legacy record = %+v", record)
	}
}

func TestDecodeIdempotencyRecordJSON(t *testing.T) {
	nodeID := uuid.New()
	createdAt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	raw, err := json.Marshal(node.IdempotencyRecord{
		NodeID:      nodeID,
		Fingerprint: "abc123",
		CreatedAt:   createdAt,
		Source:      "mcp",
	})
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}

	record, err := decodeIdempotencyRecord("new", raw)
	if err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if record.Key != "new" || record.NodeID != nodeID || record.Fingerprint != "abc123" || record.Source != "mcp" {
		t.Fatalf("json record = %+v", record)
	}
}
