package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/token"
)

// countingValidator returns a token record for each known bearer and counts
// validation calls to check how often the token cache reads the store.
type countingValidator struct {
	records map[string]*token.Token
	calls   atomic.Int64
}

func (c *countingValidator) Validate(_ context.Context, raw string) (*token.Token, error) {
	c.calls.Add(1)
	record, ok := c.records[raw]
	if !ok {
		return nil, domain.ErrUnauthenticated
	}

	copied := *record

	return &copied, nil
}

func liveToken(expiresAt *time.Time) *token.Token {
	return &token.Token{
		ID:        uuid.Must(uuid.NewV7()),
		UserID:    uuid.Must(uuid.NewV7()),
		Label:     "cached",
		LastUsed:  nil,
		ExpiresAt: expiresAt,
		CreatedAt: time.Time{},
	}
}

// TestCachedTokenValidatorAsksOncePerLifetime checks that a second lookup
// within the cache lifetime does not read the store again and returns the
// same token and user IDs.
func TestCachedTokenValidatorAsksOncePerLifetime(t *testing.T) {
	store := &countingValidator{records: map[string]*token.Token{"tack_a": liveToken(nil)}}
	cached := NewCachedTokenValidator(store, time.Minute, 16)
	ctx := context.Background()

	first, err := cached.Validate(ctx, "tack_a")
	if err != nil {
		t.Fatalf("first validate: %v", err)
	}

	second, err := cached.Validate(ctx, "tack_a")
	if err != nil {
		t.Fatalf("second validate: %v", err)
	}

	if calls := store.calls.Load(); calls != 1 {
		t.Fatalf("store asked %d times, want 1", calls)
	}

	if first.ID != second.ID || first.UserID != second.UserID {
		t.Fatalf("cached record differs: %+v vs %+v", first, second)
	}
}

// TestCachedTokenValidatorForgetsAfterTheLifetime checks that a lookup after
// expiry reads the store again and rejects a token the store removed.
func TestCachedTokenValidatorForgetsAfterTheLifetime(t *testing.T) {
	store := &countingValidator{records: map[string]*token.Token{"tack_b": liveToken(nil)}}
	cached := NewCachedTokenValidator(store, 40*time.Millisecond, 16)
	ctx := context.Background()

	if _, err := cached.Validate(ctx, "tack_b"); err != nil {
		t.Fatalf("validate: %v", err)
	}

	delete(store.records, "tack_b")
	time.Sleep(60 * time.Millisecond)
	if _, err := cached.Validate(ctx, "tack_b"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("a revoked token was accepted after the lifetime: err = %v", err)
	}

	if calls := store.calls.Load(); calls != 2 {
		t.Fatalf("store asked %d times, want 2", calls)
	}
}

// TestCachedTokenValidatorHonorsTheTokensOwnExpiry checks that the validator
// rejects a cached token after expires_at even if the cache lifetime has not
// expired.
func TestCachedTokenValidatorHonorsTheTokensOwnExpiry(t *testing.T) {
	expiresAt := time.Now().Add(30 * time.Millisecond)
	store := &countingValidator{records: map[string]*token.Token{"tack_c": liveToken(&expiresAt)}}
	cached := NewCachedTokenValidator(store, time.Hour, 16)
	ctx := context.Background()

	if _, err := cached.Validate(ctx, "tack_c"); err != nil {
		t.Fatalf("validate before expiry: %v", err)
	}

	delete(store.records, "tack_c")
	time.Sleep(50 * time.Millisecond)
	if _, err := cached.Validate(ctx, "tack_c"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("an expired token was accepted from the cache: err = %v", err)
	}
}

// TestCachedTokenValidatorNeverCachesARefusal checks that the validator
// accepts a token on the next lookup after the store creates it following a
// refusal.
func TestCachedTokenValidatorNeverCachesARefusal(t *testing.T) {
	store := &countingValidator{records: map[string]*token.Token{}}
	cached := NewCachedTokenValidator(store, time.Minute, 16)
	ctx := context.Background()

	if _, err := cached.Validate(ctx, "tack_d"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("unknown token: err = %v, want unauthenticated", err)
	}

	store.records["tack_d"] = liveToken(nil)
	if _, err := cached.Validate(ctx, "tack_d"); err != nil {
		t.Fatalf("a token created after a refusal was refused: %v", err)
	}
}

// TestCachedTokenValidatorStaysBounded checks that the cache does not exceed
// its configured entry limit.
func TestCachedTokenValidatorStaysBounded(t *testing.T) {
	store := &countingValidator{records: map[string]*token.Token{}}

	for i := range 10 {
		store.records["tack_"+string(rune('a'+i))] = liveToken(nil)
	}

	cached := NewCachedTokenValidator(store, time.Minute, 4)
	for raw := range store.records {
		if _, err := cached.Validate(context.Background(), raw); err != nil {
			t.Fatalf("validate %s: %v", raw, err)
		}
	}

	cached.cache.mu.Lock()
	size := cached.cache.order.Len()
	cached.cache.mu.Unlock()
	if size > 4 {
		t.Fatalf("cache holds %d entries, want at most 4", size)
	}
}

// TestBearerRefusesAWrappedUnauthenticated checks that Bearer returns HTTP
// 401 when CachedTokenValidator refuses an unknown token.
func TestBearerRefusesAWrappedUnauthenticated(t *testing.T) {
	store := &countingValidator{records: map[string]*token.Token{}}
	cached := NewCachedTokenValidator(store, time.Minute, 16)
	_, status := driveAuth(t, bearerFor(cached, fixedOrgs{orgs: nil, err: nil}), "tack_unknown")
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}
