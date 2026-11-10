package auth

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/org"
)

// countingMembers substitutes for org_members in tests and counts
// ListOrgIDsForUser calls.
type countingMembers struct {
	orgs  map[uuid.UUID][]uuid.UUID
	reads atomic.Int64
}

func (c *countingMembers) ListOrgIDsForUser(
	_ context.Context,
	userID uuid.UUID,
) ([]uuid.UUID, error) {
	c.reads.Add(1)
	return append([]uuid.UUID(nil), c.orgs[userID]...), nil
}

func (c *countingMembers) ListMembers(context.Context, uuid.UUID) ([]*org.Member, error) {
	return nil, nil
}

// TestCachedMembersReadsOncePerLifetime checks TACK-505. Repeated lookups
// within the cache lifetime return the same organizations and call the
// substitute store once.
func TestCachedMembersReadsOncePerLifetime(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	orgID := uuid.Must(uuid.NewV7())
	store := &countingMembers{orgs: map[uuid.UUID][]uuid.UUID{userID: {orgID}}}
	cached := NewCachedMembers(store, time.Minute, 16)
	ctx := context.Background()

	for range 3 {
		orgIDs, err := cached.ListOrgIDsForUser(ctx, userID)
		if err != nil {
			t.Fatalf("list: %v", err)
		}

		if len(orgIDs) != 1 || orgIDs[0] != orgID {
			t.Fatalf("orgs = %v, want [%s]", orgIDs, orgID)
		}
	}

	if reads := store.reads.Load(); reads != 1 {
		t.Fatalf("store read %d times, want 1", reads)
	}
}

// TestCachedMembersForgetsAfterTheLifetime checks that the next lookup after
// expiry returns a membership change from the store.
func TestCachedMembersForgetsAfterTheLifetime(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	orgID := uuid.Must(uuid.NewV7())
	store := &countingMembers{orgs: map[uuid.UUID][]uuid.UUID{userID: {orgID}}}
	cached := NewCachedMembers(store, 40*time.Millisecond, 16)
	ctx := context.Background()

	if _, err := cached.ListOrgIDsForUser(ctx, userID); err != nil {
		t.Fatalf("list: %v", err)
	}

	store.orgs[userID] = nil
	time.Sleep(60 * time.Millisecond)
	orgIDs, err := cached.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		t.Fatalf("list after lifetime: %v", err)
	}

	if len(orgIDs) != 0 || store.reads.Load() != 2 {
		t.Fatalf(
			"after the lifetime: orgs %v, reads %d; want none and 2",
			orgIDs,
			store.reads.Load(),
		)
	}
}

// TestCachedMembersHandsOutCopies checks that a caller's changes to its
// organization set do not change the set the next caller reads.
func TestCachedMembersHandsOutCopies(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	orgID := uuid.Must(uuid.NewV7())
	store := &countingMembers{orgs: map[uuid.UUID][]uuid.UUID{userID: {orgID}}}
	cached := NewCachedMembers(store, time.Minute, 16)
	ctx := context.Background()

	first, err := cached.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	first[0] = uuid.Nil
	second, err := cached.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		t.Fatalf("list again: %v", err)
	}

	if second[0] != orgID {
		t.Fatalf("a caller's write reached the cache: %v", second)
	}
}
