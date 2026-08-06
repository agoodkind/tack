package tools

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
)

func TestUniqueMatchReportsSortedCandidates(t *testing.T) {
	firstID := uuid.New()
	secondID := uuid.New()
	matches := map[uuid.UUID]struct{}{firstID: {}, secondID: {}}
	_, firstErr := uniqueMatch(matches, "reference", "FAN-13")
	_, secondErr := uniqueMatch(matches, "reference", "FAN-13")
	if firstErr == nil {
		t.Fatal("uniqueMatch: got nil error")
	}
	if firstErr.Error() != secondErr.Error() {
		t.Fatalf("errors differ: %q and %q", firstErr, secondErr)
	}
	if !errors.Is(firstErr, domain.ErrInvalidArgument) {
		t.Fatalf("uniqueMatch error = %v, want ErrInvalidArgument", firstErr)
	}
	if !strings.Contains(firstErr.Error(), firstID.String()) || !strings.Contains(firstErr.Error(), secondID.String()) {
		t.Fatalf("uniqueMatch error = %v, want both UUIDs", firstErr)
	}
}

func TestUniqueMatchZeroAndOne(t *testing.T) {
	_, err := uniqueMatch(map[uuid.UUID]struct{}{}, "reference", "FAN-13")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("zero matches error = %v, want ErrNotFound", err)
	}
	wantID := uuid.New()
	gotID, err := uniqueMatch(map[uuid.UUID]struct{}{wantID: {}}, "reference", "FAN-13")
	if err != nil {
		t.Fatalf("one match: %v", err)
	}
	if gotID != wantID {
		t.Fatalf("one match = %s, want %s", gotID, wantID)
	}
}
