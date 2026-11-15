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

// captureRecorder stores authentication events for organization assertions.
type captureRecorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (c *captureRecorder) Record(_ context.Context, event audit.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)

	return nil
}

func (c *captureRecorder) tokenUsed(t *testing.T) audit.Event {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, event := range c.events {
		if event.Verb == string(audit.VerbAuthTokenUsed) {
			return event
		}
	}

	t.Fatal("middleware did not record an auth.token_used event")

	return audit.Event{}
}

// fixedOrgs returns a fixed membership list or lookup error.
type fixedOrgs struct {
	orgs []uuid.UUID
	err  error
}

func (f fixedOrgs) ListOrgIDsForUser(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return f.orgs, f.err
}

// fixedValidator accepts every bearer as the specified user and token.
type fixedValidator struct {
	userID  uuid.UUID
	tokenID uuid.UUID
}

func (f fixedValidator) Validate(context.Context, string) (*token.Token, error) {
	return &token.Token{
		ID:        f.tokenID,
		UserID:    f.userID,
		Label:     "test",
		LastUsed:  nil,
		ExpiresAt: nil,
		CreatedAt: time.Time{},
	}, nil
}

// driveAuth sends an authenticated HTTP request to the middleware and returns
// the recorded auth.token_used event and HTTP response status.
func driveAuth(
	t *testing.T,
	build func(audit.Recorder) func(http.Handler) http.Handler,
	bearer string,
) (audit.Event, int) {
	t.Helper()
	recorder := &captureRecorder{}
	middleware := build(recorder)
	next := http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	)
	server := httptest.NewServer(middleware(next))
	t.Cleanup(server.Close)
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		server.URL,
		nil,
	)
	if err != nil {
		t.Fatalf("create authentication request: %v", err)
	}

	request.Header.Set("Authorization", "Bearer "+bearer)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send authentication request: %v", err)
	}

	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusOK {
		return audit.Event{}, response.StatusCode
	}

	return recorder.tokenUsed(t), response.StatusCode
}

func bearerFor(
	tokens TokenValidator,
	orgs OrgLister,
) func(audit.Recorder) func(http.Handler) http.Handler {
	return func(recorder audit.Recorder) func(http.Handler) http.Handler {
		return Bearer(tokens, orgs, recorder)
	}
}

func devBearerFor(orgs OrgLister) func(audit.Recorder) func(http.Handler) http.Handler {
	return func(recorder audit.Recorder) func(http.Handler) http.Handler {
		return DevBearer(orgs, recorder)
	}
}

// TestDevBearerStampsSoleOrg checks the actor and sole organization in the event.
func TestDevBearerStampsSoleOrg(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	orgID := uuid.Must(uuid.NewV7())
	event, status := driveAuth(
		t,
		devBearerFor(fixedOrgs{
			orgs: []uuid.UUID{orgID},
			err:  nil,
		}),
		userID.String(),
	)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	if event.Context.OrgID != orgID {
		t.Fatalf("token_used org = %s, want %s", event.Context.OrgID, orgID)
	}

	if event.Actor.ID != userID {
		t.Fatalf("token_used actor = %s, want %s", event.Actor.ID, userID)
	}
}

// TestDevBearerUsesSystemOrgForAmbiguousOrgs checks the system organization
// when the actor does not have exactly one membership.
func TestDevBearerUsesSystemOrgForAmbiguousOrgs(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())

	multipleOrgIDs := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	for name, orgs := range map[string][]uuid.UUID{"none": nil, "two": multipleOrgIDs} {
		event, status := driveAuth(
			t,
			devBearerFor(fixedOrgs{
				orgs: orgs,
				err:  nil,
			}),
			userID.String(),
		)
		if status != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", name, status)
		}

		if event.Context.OrgID != audit.SystemOrgID() {
			t.Fatalf("%s: token_used org = %s, want the system org", name, event.Context.OrgID)
		}
	}
}

// TestDevBearerLookupFailureNeverFailsAuth checks successful authentication
// and a system-organization event after a membership lookup error.
func TestDevBearerLookupFailureNeverFailsAuth(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	event, status := driveAuth(
		t,
		devBearerFor(fixedOrgs{
			orgs: nil,
			err:  errors.New("membership store down"),
		}),
		userID.String(),
	)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	if event.Context.OrgID != audit.SystemOrgID() {
		t.Fatalf("token_used org = %s, want the system org", event.Context.OrgID)
	}
}
