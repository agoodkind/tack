package integration_test

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

const (
	// insufficientPrivilegeState is SQLSTATE 42501 for an operation that lacks required privileges.
	insufficientPrivilegeState = "42501"
	// largeValueBytes sets the approximate size of each synthetic description to 80 KiB.
	largeValueBytes = 80 * 1024
)

var (
	// outboxFunctionQuery checks tack_migrator membership in ops_outbox_owner,
	// reads the owner of public.ops_outbox_write_encoded, and checks
	// whether ops_outbox_owner has EXECUTE on that function.
	//go:embed testdata/runtime_audit_outbox_function.sql
	outboxFunctionQuery string
	// outboxDirectInsert inserts one row directly into public.ops_outbox.
	//go:embed testdata/runtime_audit_outbox_direct_insert.sql
	outboxDirectInsert string
	// loginRoleStatement contains CREATE ROLE with placeholders for the login,
	// password, and base role.
	//go:embed testdata/runtime_audit_login_role.sql
	loginRoleStatement string
	// dropRoleStatement drops a login role.
	//go:embed testdata/runtime_audit_drop_role.sql
	dropRoleStatement string
)

// largeChangeEvent returns a synthetic format 2 change event with old and new
// description values of about largeValueBytes bytes each.
func largeChangeEvent(orgID uuid.UUID) audit.Event {
	target := audit.Target{
		Kind: audit.TargetKindNode, NodeType: "issue", Reference: "SYN-LARGE", Name: "Synthetic large issue", URI: "",
		RelationType: "", ID: uuid.Must(uuid.NewV7()), SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	event := completeEvent(orgID, userActor(), target, audit.OperationDatabaseChange, audit.PhaseChange, audit.OutcomeCompleted)
	before, _ := json.Marshal(map[string]string{"description": strings.Repeat("old synthetic line\n", largeValueBytes/len("old synthetic line\n"))})
	after, _ := json.Marshal(map[string]string{"description": strings.Repeat("new synthetic line\n", largeValueBytes/len("new synthetic line\n"))})
	event.Delta = &audit.Delta{Before: before, After: after, Changed: []string{"description"}}
	return event
}

// TestRuntimeAuditOutboxWriteFunction checks function ownership and revoked migrator membership.
// It writes a large event as an app_auth login, requires privilege denial for
// a direct outbox insert, and checks the exact part and manifest messages from ReadBatch.
func TestRuntimeAuditOutboxWriteFunction(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	admin, err := pgxpool.New(t.Context(), ledgerDSN)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	t.Cleanup(admin.Close)
	var member, ownerExecutes bool
	var owner string
	if err := admin.QueryRow(t.Context(), outboxFunctionQuery).Scan(&member, &owner, &ownerExecutes); err != nil {
		t.Fatalf("read the outbox function owner: %v", err)
	}
	if member || owner != "ops_outbox_owner" || !ownerExecutes {
		t.Fatalf("tack_migrator membership in ops_outbox_owner = %t, function owner = %s, owner execute = %t; want false, ops_outbox_owner, true",
			member, owner, ownerExecutes)
	}

	application, err := pgxpool.New(t.Context(), roleLoginDSN(t, admin, ledgerDSN, "app_auth"))
	if err != nil {
		t.Fatalf("application pool: %v", err)
	}
	t.Cleanup(application.Close)
	encoded, err := audit.EncodeEvent(largeChangeEvent(uuid.Must(uuid.NewV7())), nil)
	if err != nil || !encoded.Multipart() {
		t.Fatalf("encode a large change event: multipart %t, %v", encoded.Multipart(), err)
	}
	t.Cleanup(func() { _ = audit.NewPoolOutbox(admin).Delete(context.WithoutCancel(t.Context()), encoded.EventID) })
	transaction, err := application.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin the application transaction: %v", err)
	}
	defer func() { _ = transaction.Rollback(context.WithoutCancel(t.Context())) }()
	if err := audit.WriteEncodedOutboxTx(t.Context(), transaction, encoded); err != nil {
		t.Fatalf("write the encoded event as the application role: %v", err)
	}
	if err := transaction.Commit(t.Context()); err != nil {
		t.Fatalf("commit the application transaction: %v", err)
	}
	_, err = application.Exec(t.Context(), outboxDirectInsert, uuid.Must(uuid.NewV7()))
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != insufficientPrivilegeState {
		t.Fatalf("direct outbox insert as the application role = %v, want SQLSTATE %s", err, insufficientPrivilegeState)
	}

	rows, err := audit.NewPoolOutbox(admin).ReadBatch(t.Context(), 1000)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	want, err := encoded.Messages()
	if err != nil {
		t.Fatalf("encode the messages: %v", err)
	}
	for _, row := range rows {
		if row.EventID == encoded.EventID {
			if !reflect.DeepEqual(row.Messages, want) {
				t.Fatalf("outbox messages for %s differ from the encoded parts and manifest", encoded.EventID)
			}
			return
		}
	}
	t.Fatalf("outbox does not contain event %s", encoded.EventID)
}

// roleLoginDSN creates a login role that inherits baseRole and returns its connection string.
// It registers cleanup to drop the role after the test.
func roleLoginDSN(t *testing.T, admin *pgxpool.Pool, adminDSN, baseRole string) string {
	t.Helper()
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("create the role secret: %v", err)
	}
	login := "tack_test_parts_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	password := hex.EncodeToString(secret)
	if _, err := admin.Exec(t.Context(), fmt.Sprintf(loginRoleStatement, login, password, baseRole)); err != nil {
		t.Fatalf("create %s: %v", login, err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.WithoutCancel(t.Context()), fmt.Sprintf(dropRoleStatement, login)) })
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parse the admin DSN: %v", err)
	}
	parsed.User = url.UserPassword(login, password)
	return parsed.String()
}
