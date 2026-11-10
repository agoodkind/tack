package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// messageKey builds a part or manifest message's Kafka key from its
// organization ID and shard.
func messageKey(t *testing.T, message []byte) []byte {
	t.Helper()
	var identity struct {
		OrgID uuid.UUID `json:"org_id"`
		Shard int16     `json:"shard"`
	}
	if err := json.Unmarshal(message, &identity); err != nil {
		t.Fatalf("decode the message identity: %v", err)
	}
	key := append([]byte{}, identity.OrgID[:]...)
	return append(key, byte((identity.Shard>>8)&0xFF), byte(identity.Shard&0xFF))
}

func alteredPart(t *testing.T, message []byte) []byte {
	t.Helper()
	var fields map[string]json.RawMessage
	var payload []byte
	if err := json.Unmarshal(message, &fields); err != nil {
		t.Fatalf("decode the part message: %v", err)
	}
	if err := json.Unmarshal(fields["payload"], &payload); err != nil || len(payload) == 0 {
		t.Fatalf("decode the part payload: %v", err)
	}
	payload[len(payload)/2] ^= 0x01
	digest := sha256.Sum256(payload)
	var err error
	if fields["payload"], err = json.Marshal(payload); err != nil {
		t.Fatalf("encode the altered payload: %v", err)
	}
	if fields["part_digest"], err = json.Marshal(hex.EncodeToString(digest[:])); err != nil {
		t.Fatalf("encode the altered digest: %v", err)
	}
	altered, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encode the altered part message: %v", err)
	}
	return altered
}
