package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
)

// produceAuditEvents records count read events through the production
// Kafka recorder and closes it, which flushes every record.
func produceAuditEvents(t *testing.T, cfg *config.Config, count int) {
	t.Helper()
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: audit.SplitBrokers(cfg.AuditKafkaBrokers), Topic: cfg.AuditKafkaTopic,
		ClientID: "tack-offsets-test", ProduceTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("open the audit producer: %v", err)
	}
	orgID := uuid.Must(uuid.NewV7())
	for range count {
		event := audit.Event{
			Verb: string(audit.VerbNodeRead), EventID: uuid.Must(uuid.NewV7()),
			Actor: audit.Actor{
				Type: audit.ActorUser, ID: uuid.Must(uuid.NewV7()), Email: "", Name: "",
				SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
			},
			Entity: audit.Entity{Type: "node", NodeType: "", ID: uuid.Must(uuid.NewV7()), Identifier: "", Name: ""},
			Context: audit.EventContext{
				OrgID: orgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
				RequestID: "", TraceID: "", Source: audit.SourceMCP, Tool: "tack_get_issue", RPC: "", Reason: "",
			},
			Delta: nil, Outcome: audit.OutcomeOK, Error: nil, IdempotencyKey: "",
			OccurredAt: clock.Now().UTC(), Extra: nil,
		}
		if err := recorder.Record(t.Context(), event); err != nil {
			t.Fatalf("record an audit event: %v", err)
		}
	}
	if err := recorder.CloseContext(t.Context()); err != nil {
		t.Fatalf("flush the audit producer: %v", err)
	}
}

// readerLoginDSN creates a LOGIN role that inherits only audit_reader and
// returns the admin DSN rewritten to connect as it.
func readerLoginDSN(t *testing.T, admin *pgxpool.Pool, adminDSN string) string {
	t.Helper()
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("role secret: %v", err)
	}
	login := "tack_test_offsets_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	password := hex.EncodeToString(secret)
	statement := "CREATE ROLE " + login + " LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '" + password + "' IN ROLE audit_reader"
	if _, err := admin.Exec(t.Context(), statement); err != nil {
		t.Fatalf("create %s: %v", login, err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+login) })
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parse the admin DSN: %v", err)
	}
	parsed.User = url.UserPassword(login, password)
	return parsed.String()
}
