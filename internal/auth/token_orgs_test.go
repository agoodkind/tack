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

// tokenOrgsStore answers a token and its holder's org set in one read and
// counts those reads, standing in for the joined ledger query.
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

func (s *tokenOrgsStore) ValidateWithOrgs(_ context.Context, raw string) (*token.Token, []uuid.UUID, error) {
	s.reads.Add(1)
	if raw != "tack_cold" {
		return nil, nil, domain.ErrUnauthenticated
	}
	copied := *s.record
	return &copied, append([]uuid.UUID(nil), s.orgIDs...), nil
}

// TestColdRequestCostsOneLedgerRead pins criterion 12 of the backup
// acceptance on a cold cache. QA measured two ledger reads for a first
// request, the token lookup and then the membership lookup; with the token
// cache primed from the joined query, the membership lookup is a cache hit
// and the auth event still carries the sole org.
func TestColdRequestCostsOneLedgerRead(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	orgID := uuid.Must(uuid.NewV7())
	store := &tokenOrgsStore{record: liveToken(nil), orgIDs: []uuid.UUID{orgID}}
	store.record.UserID = userID
	members := &countingMembers{orgs: map[uuid.UUID][]uuid.UUID{userID: {orgID}}}

	cachedMembers := NewCachedMembers(members, time.Minute, 16)
	cachedTokens := NewCachedTokenValidator(store, time.Minute, 16)
	cachedTokens.PrimeMembers(cachedMembers)

	ev, status := driveAuth(t, Bearer(cachedTokens, cachedMembers), "tack_cold")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if reads := store.reads.Load() + members.reads.Load(); reads != 1 {
		t.Fatalf("a cold request made %d ledger reads (token %d, membership %d), want 1",
			reads, store.reads.Load(), members.reads.Load())
	}
	if ev.Context.OrgID != orgID {
		t.Fatalf("token_used org = %s, want %s", ev.Context.OrgID, orgID)
	}
}

// TestColdRefusalCachesNoMembership pins that a refused token primes
// nothing: the joined query found no holder, so no org set may be stored.
func TestColdRefusalCachesNoMembership(t *testing.T) {
	store := &tokenOrgsStore{record: liveToken(nil), orgIDs: nil}
	members := &countingMembers{orgs: map[uuid.UUID][]uuid.UUID{}}
	cachedMembers := NewCachedMembers(members, time.Minute, 16)
	cachedTokens := NewCachedTokenValidator(store, time.Minute, 16)
	cachedTokens.PrimeMembers(cachedMembers)

	if _, status := driveAuth(t, Bearer(cachedTokens, cachedMembers), "tack_unknown"); status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
	cachedMembers.cache.mu.Lock()
	held := cachedMembers.cache.order.Len()
	cachedMembers.cache.mu.Unlock()
	if held != 0 {
		t.Fatalf("membership cache holds %d entries after a refusal, want 0", held)
	}
}
