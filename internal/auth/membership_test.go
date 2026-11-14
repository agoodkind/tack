package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
)

// countingOrgs returns a membership set and counts middleware lookups.
type countingOrgs struct {
	orgs  []uuid.UUID
	err   error
	calls atomic.Int64
}

func (c *countingOrgs) ListOrgIDsForUser(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	c.calls.Add(1)
	return c.orgs, c.err
}

// membershipSeenByNext sends an authenticated request through middleware and
// returns the next handler's context memberships and their presence flag.
func membershipSeenByNext(
	t *testing.T,
	middleware func(http.Handler) http.Handler,
) ([]uuid.UUID, bool) {
	t.Helper()
	var seen []uuid.UUID
	var attached bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, attached = OrgMembership(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(middleware(next))
	t.Cleanup(server.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	req.Header.Set("Authorization", "Bearer raw-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}

	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	return seen, attached
}

// TestBearerAttachesTheMembershipItReadOnce checks that middleware queries
// membership once and stores the result in the handler context (TACK-503).
func TestBearerAttachesTheMembershipItReadOnce(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	orgs := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	lister := &countingOrgs{orgs: orgs, err: nil}

	seen, attached := membershipSeenByNext(
		t,
		Bearer(fixedValidator{userID: userID}, lister, audit.NoopRecorder{}),
	)

	if calls := lister.calls.Load(); calls != 1 {
		t.Fatalf("membership read %d times, want 1", calls)
	}

	if !attached || len(seen) != 2 || seen[0] != orgs[0] || seen[1] != orgs[1] {
		t.Fatalf("handler saw membership %v (attached %v), want %v", seen, attached, orgs)
	}
}

// TestBearerOmitsMembershipAfterLookupFailure checks that lookup failures
// do not install an empty membership set in the handler context. The handler
// must perform its own membership check before returning protected data.
func TestBearerOmitsMembershipAfterLookupFailure(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	lister := &countingOrgs{orgs: nil, err: errors.New("membership store down")}

	seen, attached := membershipSeenByNext(
		t,
		Bearer(fixedValidator{userID: userID}, lister, audit.NoopRecorder{}),
	)

	if attached || seen != nil {
		t.Fatalf("a failed read attached membership %v", seen)
	}
}
