package auth

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/token"
)

// tokenOrgsStore returns a token and its user's org set in one read and
// counts reads to represent the joined ledger query.
type tokenOrgsStore struct {
	record *token.Token
	orgIDs []uuid.UUID
	reads  atomic.Int64
}

func (s *tokenOrgsStore) Validate(context.Context, string) (*token.Token, error) {
	s.reads.Add(1)
	copied := *s.record

	return &copied, nil
}

func (s *tokenOrgsStore) ValidateWithOrgs(
	_ context.Context,
	raw string,
) (*token.Token, []uuid.UUID, error) {
	s.reads.Add(1)
	if raw != "tack_cold" {
		return nil, nil, domain.ErrUnauthenticated
	}

	copied := *s.record

	return &copied, append([]uuid.UUID(nil), s.orgIDs...), nil
}

// TestColdRequestCostsOneLedgerRead checks criterion 12 with empty caches.
// The joined query primes the membership cache. Authentication must use one
// ledger read and record the user's sole org in the auth event.
func TestColdRequestCostsOneLedgerRead(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	orgID := uuid.Must(uuid.NewV7())
	store := &tokenOrgsStore{record: liveToken(nil), orgIDs: []uuid.UUID{orgID}}
	store.record.UserID = userID
	members := &countingMembers{orgs: map[uuid.UUID][]uuid.UUID{userID: {orgID}}}

	cachedMembers := NewCachedMembers(members, time.Minute, 16)
	cachedTokens := NewCachedTokenValidator(store, time.Minute, 16)
	cachedTokens.PrimeMembers(cachedMembers)

	ev, status := driveAuth(t, bearerFor(cachedTokens, cachedMembers), "tack_cold")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	if reads := store.reads.Load() + members.reads.Load(); reads != 1 {
		t.Fatalf(
			"a cold request made %d ledger reads (token %d, membership %d), want 1",
			reads,
			store.reads.Load(),
			members.reads.Load(),
		)
	}

	if ev.Context.OrgID != orgID {
		t.Fatalf("token_used org = %s, want %s", ev.Context.OrgID, orgID)
	}
}

// TestColdRefusalPreservesEmptyMembershipCache checks that rejecting an unknown token
// does not add entries to the membership cache.
func TestColdRefusalPreservesEmptyMembershipCache(t *testing.T) {
	store := &tokenOrgsStore{record: liveToken(nil), orgIDs: nil}
	members := &countingMembers{orgs: map[uuid.UUID][]uuid.UUID{}}
	cachedMembers := NewCachedMembers(members, time.Minute, 16)
	cachedTokens := NewCachedTokenValidator(store, time.Minute, 16)
	cachedTokens.PrimeMembers(cachedMembers)

	if _, status := driveAuth(
		t,
		bearerFor(cachedTokens, cachedMembers),
		"tack_unknown",
	); status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}

	cachedMembers.cache.mu.Lock()
	held := cachedMembers.cache.order.Len()
	cachedMembers.cache.mu.Unlock()
	if held != 0 {
		t.Fatalf("membership cache stores %d entries after a refusal, want 0", held)
	}
}
