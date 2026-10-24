package auth

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/org"
)

// countingMembers is a membership store that counts its reads, standing in
// for the org_members table.
type countingMembers struct {
	orgs  map[uuid.UUID][]uuid.UUID
	reads atomic.Int64
}

func (c *countingMembers) ListOrgIDsForUser(_ context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	c.reads.Add(1)
	return append([]uuid.UUID(nil), c.orgs[userID]...), nil
}

func (c *countingMembers) ListMembers(context.Context, uuid.UUID) ([]*org.Member, error) {
	return nil, nil
}

// TestCachedMembersReadsOncePerLifetime pins TACK-505: the second read
// within the lifetime costs no call to the store, and the set is the same.
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

// TestCachedMembersForgetsAfterTheLifetime pins that a change made elsewhere
// reaches this instance within one lifetime.
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
		t.Fatalf("after the lifetime: orgs %v, reads %d; want none and 2", orgIDs, store.reads.Load())
	}
}

// TestCachedMembersHandsOutCopies pins that a caller mutating its set does
// not change what the next caller reads.
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
