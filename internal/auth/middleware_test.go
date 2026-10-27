package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/token"
)

// captureRecorder keeps every auth event the middleware records so a test
// can read the stamped org back.
type captureRecorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (c *captureRecorder) Record(_ context.Context, ev audit.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
	return nil
}

func (c *captureRecorder) tokenUsed(t *testing.T) audit.Event {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ev := range c.events {
		if ev.Verb == string(audit.VerbAuthTokenUsed) {
			return ev
		}
	}
	t.Fatal("no auth.token_used event recorded")
	return audit.Event{}
}

// fixedOrgs serves a canned membership answer, standing in for org_members.
type fixedOrgs struct {
	orgs []uuid.UUID
	err  error
}

func (f fixedOrgs) ListOrgIDsForUser(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return f.orgs, f.err
}

// fixedValidator accepts every bearer as one user, standing in for the token
// table.
type fixedValidator struct {
	userID  uuid.UUID
	tokenID uuid.UUID
}

func (f fixedValidator) Validate(context.Context, string) (*token.Token, error) {
	return &token.Token{ID: f.tokenID, UserID: f.userID, Label: "test", LastUsed: nil, ExpiresAt: nil, CreatedAt: time.Time{}}, nil
}

// driveAuth sends one authenticated request through the middleware that
// build returns for recorder, and returns the recorded auth.token_used event
// and the response status.
func driveAuth(t *testing.T, build func(audit.Recorder) func(http.Handler) http.Handler, bearer string) (audit.Event, int) {
	t.Helper()
	recorder := &captureRecorder{}
	middleware := build(recorder)

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	server := httptest.NewServer(middleware(next))
	t.Cleanup(server.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		return audit.Event{}, resp.StatusCode
	}
	return recorder.tokenUsed(t), resp.StatusCode
}

// bearerFor returns a builder of the Bearer middleware over tokens and orgs.
func bearerFor(tokens TokenValidator, orgs OrgLister) func(audit.Recorder) func(http.Handler) http.Handler {
	return func(recorder audit.Recorder) func(http.Handler) http.Handler {
		return Bearer(tokens, orgs, recorder)
	}
}

// devBearerFor returns a builder of the DevBearer middleware over orgs.
func devBearerFor(orgs OrgLister) func(audit.Recorder) func(http.Handler) http.Handler {
	return func(recorder audit.Recorder) func(http.Handler) http.Handler {
		return DevBearer(orgs, recorder)
	}
}

// TestDevBearerStampsSoleOrg pins TACK-461's forward fix: an auth event for
// an actor who belongs to exactly one org carries that org instead of nil.
func TestDevBearerStampsSoleOrg(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	orgID := uuid.Must(uuid.NewV7())
	ev, status := driveAuth(t, devBearerFor(fixedOrgs{orgs: []uuid.UUID{orgID}, err: nil}), userID.String())
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if ev.Context.OrgID != orgID {
		t.Fatalf("token_used org = %s, want %s", ev.Context.OrgID, orgID)
	}
	if ev.Actor.ID != userID {
		t.Fatalf("token_used actor = %s, want %s", ev.Actor.ID, userID)
	}
}

// TestDevBearerUsesSystemOrgForAmbiguousOrgs checks that an actor with zero
// or several memberships produces a token_used event in the system org.
func TestDevBearerUsesSystemOrgForAmbiguousOrgs(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	two := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	for name, orgs := range map[string][]uuid.UUID{"none": nil, "two": two} {
		ev, status := driveAuth(t, devBearerFor(fixedOrgs{orgs: orgs, err: nil}), userID.String())
		if status != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", name, status)
		}
		if ev.Context.OrgID != audit.SystemOrgID() {
			t.Fatalf("%s: token_used org = %s, want the system org", name, ev.Context.OrgID)
		}
	}
}

// TestDevBearerLookupFailureNeverFailsAuth checks that a membership lookup
// error produces a token_used event in the system org and the request still
// succeeds.
func TestDevBearerLookupFailureNeverFailsAuth(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	ev, status := driveAuth(t, devBearerFor(fixedOrgs{orgs: nil, err: errors.New("membership store down")}), userID.String())
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if ev.Context.OrgID != audit.SystemOrgID() {
		t.Fatalf("token_used org = %s, want the system org", ev.Context.OrgID)
	}
}

// TestBearerStampsSoleOrg covers the production token path with the same
// sole-org stamp, and pins that the accepted token's id rides on the event,
// which is what the audit-consumer projects the token's last use from
// (TACK-502).
func TestBearerStampsSoleOrg(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	orgID := uuid.Must(uuid.NewV7())
	tokenID := uuid.Must(uuid.NewV7())
	build := bearerFor(fixedValidator{userID: userID, tokenID: tokenID}, fixedOrgs{orgs: []uuid.UUID{orgID}, err: nil})
	ev, status := driveAuth(t, build, "raw-token")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if ev.Context.OrgID != orgID {
		t.Fatalf("token_used org = %s, want %s", ev.Context.OrgID, orgID)
	}
	if ev.Context.APITokenID != tokenID {
		t.Fatalf("token_used token = %s, want %s", ev.Context.APITokenID, tokenID)
	}
}
