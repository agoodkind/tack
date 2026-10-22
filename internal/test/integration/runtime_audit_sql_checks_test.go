package integration_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/token"
	"goodkind.io/tack/internal/domain/user"
)

const (
	sqlAuditMemberRole    = 15
	sqlAuditAdminRole     = 20
	sqlAuditTokenLabel    = "runtime audit"
	sqlAuditTokenURI      = "tack:api_token/"
	sqlAuditFailureCode   = "not_found"
	sqlAuditMemberChanged = "role"

	sqlAuditEmailField       = "/email"
	sqlAuditDisplayNameField = "/display_name"
)

type memberStep struct {
	operation audit.Event
	role      int
}

type tokenRecord struct {
	ID        uuid.UUID  `json:"id"`
	UserID    uuid.UUID  `json:"user_id"`
	Label     string     `json:"label"`
	ExpiresAt *time.Time `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
}

type memberRecord struct {
	ID        uuid.UUID `json:"id"`
	OrgID     uuid.UUID `json:"org_id"`
	UserID    uuid.UUID `json:"user_id"`
	Role      int       `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// requireUserChange waits for the ledger row of the user creation event.
// It requires Changed to equal /email and /display_name in that order.
// It also requires the row to omit holder.Email and holder.DisplayName.
func requireUserChange(t *testing.T, stack runtimeAuditStack, sent audit.Event, holder *user.User) {
	t.Helper()
	row := requireLedgerChange(t, stack, sent)
	if !slices.Equal(row.Delta.Changed, []string{sqlAuditEmailField, sqlAuditDisplayNameField}) {
		t.Fatalf("user creation event has Delta.Changed %v; the test requires [/email /display_name]", row.Delta.Changed)
	}
	requireNoSecrets(t, row, []string{holder.Email, holder.DisplayName})
}

func requireMembership(t *testing.T, members *postgres.OrgMemberRepo, orgID, userID uuid.UUID) *org.Member {
	t.Helper()
	listed, err := members.ListMembers(t.Context(), orgID)
	if err != nil || len(listed) != 1 || listed[0].UserID != userID || listed[0].Role != sqlAuditAdminRole {
		t.Fatalf("ListMembers(%s) = %+v, %v; want one member for userID with Role sqlAuditAdminRole and nil error", orgID, listed, err)
	}
	return listed[0]
}

func requireMemberTarget(t *testing.T, row audit.Row, want memberRecord) {
	t.Helper()
	target := row.Context.Target
	if target == nil || target.Kind != audit.TargetKindRelationship || target.ID != want.ID ||
		target.SourceID != want.OrgID || target.TargetID != want.UserID {
		t.Fatalf("membership target = %+v, want audit.TargetKindRelationship with ID %s, TargetID %s, and SourceID %s", target, want.ID, want.UserID, want.OrgID)
	}
}

func requireMemberRecord(t *testing.T, raw json.RawMessage, want memberRecord) {
	t.Helper()
	var got memberRecord
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode membership value %s: got error %v, want nil", raw, err)
	}
	if got.ID != want.ID || got.OrgID != want.OrgID || got.UserID != want.UserID || got.Role != want.Role ||
		!got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("membership value = %+v, want %+v", got, want)
	}
}

func requireTokenTarget(t *testing.T, row audit.Row, tokenID uuid.UUID) {
	t.Helper()
	target := row.Context.Target
	if target == nil || target.Kind != audit.TargetKindResource || target.ID != tokenID ||
		target.URI != sqlAuditTokenURI+tokenID.String() || target.Name != sqlAuditTokenLabel {
		t.Fatalf("token target = %+v, want audit.TargetKindResource with ID %s, URI sqlAuditTokenURI+tokenID.String(), and Name sqlAuditTokenLabel", target, tokenID)
	}
}

func requireTokenRecord(t *testing.T, raw json.RawMessage, want *token.Token) {
	t.Helper()
	var got tokenRecord
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode token value %s: got error %v, want nil", raw, err)
	}
	if got.ID != want.ID || got.UserID != want.UserID || got.Label != want.Label ||
		got.ExpiresAt != nil || want.ExpiresAt != nil || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("token value = %+v, want %+v", got, want)
	}
}

// requireNoSecrets encodes the ledger row as JSON and fails if the encoding
// contains any value in secrets.
func requireNoSecrets(t *testing.T, row audit.Row, secrets []string) {
	t.Helper()
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("encode ledger row %s: got error %v, want nil", row.EventID, err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("ledger row %s contains a value from secrets; the test requires the row to omit every value in secrets", row.EventID)
		}
	}
}
