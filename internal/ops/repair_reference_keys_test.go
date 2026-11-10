package ops

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/domain/node"
)

type referenceKeyWriteSpy struct{ owners map[string]uuid.UUID }

func (s *referenceKeyWriteSpy) SetReferenceKeys(_ context.Context, _ uuid.UUID, nodeID uuid.UUID, keys []node.ReferenceKey) error {
	for _, key := range keys {
		identity := key.TemplateName + ":" + key.Encoded
		if owner, exists := s.owners[identity]; exists && owner != nodeID {
			return fmt.Errorf("reference %s already belongs to %s", identity, owner)
		}
		s.owners[identity] = nodeID
	}
	return nil
}

func TestWriteReferenceKeysByNodeKeepsTheLowestIDOnConflict(t *testing.T) {
	firstID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	secondID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	key := node.ReferenceKey{TemplateName: "reference", Encoded: "TACK-1"}
	writer := &referenceKeyWriteSpy{owners: make(map[string]uuid.UUID)}
	written, err := writeReferenceKeysByNode(context.Background(), writer, uuid.New(), map[uuid.UUID][]node.ReferenceKey{secondID: {key}, firstID: {key}})
	if err == nil {
		t.Fatal("duplicate key write succeeded")
	}
	if owner := writer.owners["reference:TACK-1"]; owner != firstID {
		t.Fatalf("duplicate key owner = %s, want %s", owner, firstID)
	}
	// The node written before the failure is reported, and the one that failed
	// is not: that split is what lets the caller record exactly what landed.
	if !written[firstID] || written[secondID] {
		t.Fatalf("written = %v, want only %s", written, firstID)
	}
}
