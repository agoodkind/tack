package auth

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// TestBearerStampsSoleOrg checks the organization and accepted token ID.
// The audit consumer uses the token ID to update its last-use timestamp.
func TestBearerStampsSoleOrg(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	orgID := uuid.Must(uuid.NewV7())
	tokenID := uuid.Must(uuid.NewV7())
	build := bearerFor(
		fixedValidator{userID: userID, tokenID: tokenID},
		fixedOrgs{orgs: []uuid.UUID{orgID}, err: nil},
	)
	event, status := driveAuth(t, build, "raw-token")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	if event.Context.OrgID != orgID {
		t.Fatalf("token_used org = %s, want %s", event.Context.OrgID, orgID)
	}

	if event.Context.APITokenID != tokenID {
		t.Fatalf("token_used token = %s, want %s", event.Context.APITokenID, tokenID)
	}
}
