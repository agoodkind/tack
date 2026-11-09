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

// tokenOrgsStore substitutes for the joined database query in tests. It
// returns a token and its user's organizations and counts lookup calls.
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

// TestColdRequestCostsOneLedgerRead checks that a cold request makes one
// combined token and membership lookup. The membership cache reuses that
// result, and the auth event records the actor's sole organization.
//
// QA measured two ledger reads for a first request: one token lookup and one
// membership lookup. This test checks the combined lookup against that
// baseline.
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

// TestColdRefusalCachesNoMembership checks that a refused token does not
// populate the membership cache when the combined lookup cannot find its
// user.
func TestColdRefusalCachesNoMembership(t *testing.T) {
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
		t.Fatalf("membership cache holds %d entries after a refusal, want 0", held)
	}
}
