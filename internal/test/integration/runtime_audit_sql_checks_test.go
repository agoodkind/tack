package integration_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/token"
)

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

func requireMembership(t *testing.T, members *postgres.OrgMemberRepo, orgID, userID uuid.UUID) *org.Member {
	t.Helper()
	listed, err := members.ListMembers(t.Context(), orgID)
	if err != nil || len(listed) != 1 || listed[0].UserID != userID || listed[0].Role != sqlAuditAdminRole {
		t.Fatalf("members of %s = %+v, %v; want one admin membership", orgID, listed, err)
	}
	return listed[0]
}

func requireMemberTarget(t *testing.T, row audit.Row, want memberRecord) {
	t.Helper()
	target := row.Context.Target
	if target == nil || target.Kind != audit.TargetKindRelationship || target.ID != want.ID ||
		target.SourceID != want.OrgID || target.TargetID != want.UserID {
		t.Fatalf("membership target = %+v, want membership %s of user %s in org %s", target, want.ID, want.UserID, want.OrgID)
	}
}

func requireMemberRecord(t *testing.T, raw json.RawMessage, want memberRecord) {
	t.Helper()
	var got memberRecord
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode membership value %s: %v", raw, err)
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
		t.Fatalf("token target = %+v, want token %s", target, tokenID)
	}
}

func requireTokenRecord(t *testing.T, raw json.RawMessage, want *token.Token) {
	t.Helper()
	var got tokenRecord
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode token value %s: %v", raw, err)
	}
	if got.ID != want.ID || got.UserID != want.UserID || got.Label != want.Label ||
		got.ExpiresAt != nil || want.ExpiresAt != nil || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("token value = %+v, want %+v", got, want)
	}
}

func requireNoSecrets(t *testing.T, row audit.Row, secrets []string) {
	t.Helper()
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("encode ledger row %s: %v", row.EventID, err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("ledger row %s contains token material", row.EventID)
		}
	}
}
