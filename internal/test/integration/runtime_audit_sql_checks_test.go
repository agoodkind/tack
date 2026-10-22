package integration_test

import (
	"context"
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
	sqlAuditMemberChanged = "/role"
)

// sqlAuditUserChanged lists the sorted JSON pointers that a user creation
// event must include in Delta.Changed.
var sqlAuditUserChanged = []string{"/display_name", "/email", "/is_active"}

// memberStep pairs a membership role with its mutation context and operation.
type memberStep struct {
	ctx       context.Context
	operation audit.Event
	role      int
}

// newMemberStep creates a mutation context and operation for principal and
// returns them with role as a membership test step.
func newMemberStep(t *testing.T, principal audit.OperatorPrincipal, role int) memberStep {
	t.Helper()
	ctx, operation := sqlAuditContext(t, principal)
	return memberStep{ctx: ctx, operation: operation, role: role}
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

// requireUserChange waits for the user creation ledger row and requires an
// empty Delta.Before and Delta.Changed equal to sqlAuditUserChanged. It
// decodes Delta.After, checks the user ID and two distinct protected references
// other than uuid.Nil, and requires the row to omit the raw email and display
// name. It returns the decoded record.
func requireUserChange(t *testing.T, stack runtimeAuditStack, sent audit.Event, holder *user.User) userRecord {
	t.Helper()
	row := requireLedgerChange(t, stack, sent)
	if row.Delta == nil || len(row.Delta.Before) != 0 || !slices.Equal(row.Delta.Changed, sqlAuditUserChanged) {
		t.Fatalf("row.Delta = %+v, want non-nil row.Delta with empty row.Delta.Before and row.Delta.Changed = sqlAuditUserChanged", row.Delta)
	}
	var record userRecord
	if err := json.Unmarshal(row.Delta.After, &record); err != nil {
		t.Fatalf("json.Unmarshal of row.Delta.After %s: error = %v, want nil", row.Delta.After, err)
	}
	if record.ID != holder.ID || record.Email.PIIRef == uuid.Nil || record.DisplayName.PIIRef == uuid.Nil ||
		record.Email.PIIRef == record.DisplayName.PIIRef {
		t.Fatalf("record = %+v, want record.ID = %s and distinct record.Email.PIIRef and record.DisplayName.PIIRef values other than uuid.Nil", record, holder.ID)
	}
	requireNoSecrets(t, row, []string{holder.Email, holder.DisplayName})
	return record
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
