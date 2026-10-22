package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"reflect"
	"testing"
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/testenv"
)

//go:embed testdata/runtime_audit_outbox_events.sql
var runtimeAuditOutboxEventsQuery string

//go:embed testdata/runtime_audit_change_login.sql
var runtimeAuditChangeLoginTemplate string

//go:embed testdata/runtime_audit_drop_login.sql
var runtimeAuditDropLoginTemplate string

const (
	sqlAuditRelayInterval = 100 * time.Millisecond
	sqlAuditRelayBatch    = 64
	sqlAuditProduceWait   = 30 * time.Second
	sqlAuditLoginPrefix   = "tack_test_change_"
)

type sqlAuditLogin struct {
	Login  string
	Secret string
}

func sqlAuditPrincipal() audit.OperatorPrincipal {
	return audit.OperatorPrincipal{
		ID: uuid.Must(uuid.NewV7()), Email: "operator@example.invalid", Name: "Operator", Source: "test",
	}
}

func sqlAuditOperation(verb audit.Verb, principal audit.OperatorPrincipal, orgID uuid.UUID) audit.Event {
	return postgres.NewChangeOperation(verb, postgres.OperatorActor(principal), orgID, audit.SourceSystem)
}

func createSQLAuditUser(t *testing.T, pool *pgxpool.Pool, principal audit.OperatorPrincipal) *user.User {
	t.Helper()
	operation := sqlAuditOperation(audit.VerbUserCreate, principal, audit.SystemOrgID())
	created, err := postgres.NewUserRepo(pool).Create(t.Context(), operation, &user.User{
		ID: uuid.Nil, Email: "sql-audit-" + uuid.NewString() + "@example.invalid",
		DisplayName: "SQL Audit Holder", AvatarURL: nil, CreatedAt: time.Time{}, UpdatedAt: time.Time{},
	})
	if err != nil {
		t.Fatalf("create the synthetic user: %v", err)
	}
	return created
}

// outboxEvents reads operator outbox events matching id as an entity ID or
// operation ID, in creation order.
func outboxEvents(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) []audit.Event {
	t.Helper()
	rows, err := pool.Query(t.Context(), runtimeAuditOutboxEventsQuery, id.String())
	if err != nil {
		t.Fatalf("read outbox events for %s: %v", id, err)
	}
	defer rows.Close()
	events := make([]audit.Event, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan an outbox event: %v", err)
		}
		var event audit.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("decode an outbox event: %v", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read outbox events for %s: %v", id, err)
	}
	return events
}

// startOutboxRelay runs the real relay to publish operator outbox events to the
// test Kafka topic until the test ends.
func startOutboxRelay(t *testing.T, stack runtimeAuditStack) {
	t.Helper()
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: []string{testenv.Kafka(t)}, Topic: stack.topic, ClientID: "tack-runtime-audit-relay",
		ProduceTimeout: sqlAuditProduceWait,
	})
	if err != nil {
		t.Fatalf("open the relay producer: %v", err)
	}
	relay, err := audit.NewRelay(audit.RelayConfig{
		Recorder: recorder, Yugabyte: audit.NewPoolOutbox(stack.admin), FoundationDB: nil,
		PollInterval: sqlAuditRelayInterval, BatchSize: sqlAuditRelayBatch,
	})
	if err != nil {
		t.Fatalf("start the relay: %v", err)
	}
	relay.Start(t.Context())
	t.Cleanup(func() { _ = relay.Close() })
}

// changeOnlyPool opens a pool for a new login that can read and change users,
// api_tokens, and org_members but cannot execute
// public.ops_outbox_write_encoded. Test cleanup drops the login.
func changeOnlyPool(t *testing.T, admin *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	suffix := make([]byte, 4)
	secret := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("login suffix: %v", err)
	}
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("login secret: %v", err)
	}
	login := sqlAuditLogin{Login: sqlAuditLoginPrefix + hex.EncodeToString(suffix), Secret: hex.EncodeToString(secret)}
	execLoginTemplate(t, admin, runtimeAuditChangeLoginTemplate, login)
	t.Cleanup(func() { execLoginTemplate(t, admin, runtimeAuditDropLoginTemplate, login) })
	parsed, err := url.Parse(admin.Config().ConnString())
	if err != nil {
		t.Fatalf("parse the ledger address: %v", err)
	}
	parsed.User = url.UserPassword(login.Login, login.Secret)
	return testenv.LedgerPool(t, parsed.String())
}

func execLoginTemplate(t *testing.T, admin *pgxpool.Pool, text string, login sqlAuditLogin) {
	t.Helper()
	parsed, err := template.New("login").Parse(text)
	if err != nil {
		t.Fatalf("parse the login statements: %v", err)
	}
	var statements bytes.Buffer
	if err := parsed.Execute(&statements, login); err != nil {
		t.Fatalf("render the login statements: %v", err)
	}
	if _, err := admin.Exec(context.WithoutCancel(t.Context()), statements.String()); err != nil {
		t.Fatalf("run the login statements for %s: %v", login.Login, err)
	}
}

// requireLedgerChange waits for the ledger row for sent and requires
// audit.OperationDatabaseChange. It compares the target, outcome, phase,
// operation ID, actor ID, action, and error with sent.
func requireLedgerChange(t *testing.T, stack runtimeAuditStack, sent audit.Event) audit.Row {
	t.Helper()
	row := stack.waitForRow(t, sent.EventID)
	if !reflect.DeepEqual(row.Context.Target, sent.Context.Target) {
		t.Fatalf("event %s target = %+v, want %+v", sent.EventID, row.Context.Target, sent.Context.Target)
	}
	if row.Outcome != sent.Outcome || row.Context.Phase != sent.Context.Phase ||
		row.Context.OperationClass != audit.OperationDatabaseChange {
		t.Fatalf("event %s outcome, phase, class = %s, %s, %s", sent.EventID, row.Outcome, row.Context.Phase, row.Context.OperationClass)
	}
	if row.Context.OperationID != sent.Context.OperationID || row.ActorID != sent.Actor.ID || row.Action != sent.Verb {
		t.Fatalf("event %s operation %s actor %s action %s", sent.EventID, row.Context.OperationID, row.ActorID, row.Action)
	}
	if !reflect.DeepEqual(row.Error, sent.Error) {
		t.Fatalf("event %s error = %+v, want %+v", sent.EventID, row.Error, sent.Error)
	}
	return row
}

func onlyEvent(t *testing.T, events []audit.Event, phase audit.Phase) audit.Event {
	t.Helper()
	if len(events) != 1 || events[0].Context.Phase != phase {
		t.Fatalf("outbox events = %+v, want one %s event", events, phase)
	}
	return events[0]
}
