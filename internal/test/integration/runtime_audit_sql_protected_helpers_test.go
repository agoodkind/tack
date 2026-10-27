package integration_test

import (
	_ "embed"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// runtimeAuditProtectedValuesQuery embeds the SQL query that reads audit.pii
// payloads and redaction flags for the requested references.
//
//go:embed testdata/runtime_audit_protected_values.sql
var runtimeAuditProtectedValuesQuery string

// auditWriterRole, auditReaderRole, and auditRedactorRole are the base roles
// for the integration test logins.
const (
	auditWriterRole   = "audit_writer"
	auditReaderRole   = "audit_reader"
	auditRedactorRole = "audit_redactor"
)

// userRecord contains the user ID and protected email and display name
// references from Delta.After.
type userRecord struct {
	ID          uuid.UUID                `json:"id"`
	Email       audit.ProtectedReference `json:"email"`
	DisplayName audit.ProtectedReference `json:"display_name"`
}

// startWriterRoleStack starts a runtime audit stack with an audit_writer
// consumer login and an audit_reader ledger login.
func startWriterRoleStack(t *testing.T) runtimeAuditStack {
	t.Helper()
	ctx := t.Context()
	adminDSN := testenv.Ledger(t)
	brokers := testenv.Kafka(t)
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("pgxpool.New for admin: error = %v, want nil", err)
	}
	t.Cleanup(admin.Close)
	topic := "audit.runtime-test-" + uuid.NewString()[:8]
	consumer, err := audit.NewConsumer(ctx, audit.ConsumerConfig{
		Brokers: []string{brokers}, Topic: topic, GroupID: "tack-runtime-audit-" + uuid.NewString()[:8],
		BatchSize: 32, PollInterval: sqlAuditRelayInterval, YugabyteDSN: roleLoginDSN(t, admin, adminDSN, auditWriterRole),
	})
	if err != nil {
		t.Fatalf("audit.NewConsumer as audit_writer: error = %v, want nil", err)
	}
	consumer.Start(ctx)
	t.Cleanup(func() { _ = consumer.Close() })
	reader, err := audit.NewReader(ctx, roleLoginDSN(t, admin, adminDSN, auditReaderRole))
	if err != nil {
		t.Fatalf("audit.NewReader as audit_reader: error = %v, want nil", err)
	}
	t.Cleanup(reader.Close)
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: []string{brokers}, Topic: topic, ClientID: "tack-runtime-audit-test", ProduceTimeout: sqlAuditProduceWait,
	})
	if err != nil {
		t.Fatalf("audit.NewKafkaRecorder error = %v, want nil", err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	return runtimeAuditStack{topic: topic, admin: admin, reader: reader, recorder: recorder}
}

// protectedRow stores one audit.pii payload and its redaction flag.
type protectedRow struct {
	Value    []byte
	Redacted bool
}

// openRedactor creates a login with the audit_redactor role and opens an
// audit.Redactor and a pgxpool.Pool with that login. It registers cleanup
// for both.
func openRedactor(t *testing.T, stack runtimeAuditStack) (*audit.Redactor, *pgxpool.Pool) {
	t.Helper()
	redactorDSN := roleLoginDSN(t, stack.admin, testenv.Ledger(t), auditRedactorRole)
	redactor, err := audit.NewRedactor(t.Context(), redactorDSN)
	if err != nil {
		t.Fatalf("audit.NewRedactor as audit_redactor: error = %v, want nil", err)
	}
	t.Cleanup(redactor.Close)
	pool, err := pgxpool.New(t.Context(), redactorDSN)
	if err != nil {
		t.Fatalf("pgxpool.New as audit_redactor: err = %v, want nil", err)
	}
	t.Cleanup(pool.Close)
	return redactor, pool
}

// protectedValues uses pool to read audit.pii payloads and redaction flags
// for refs and returns them in a map keyed by reference.
func protectedValues(t *testing.T, pool *pgxpool.Pool, refs []uuid.UUID) map[uuid.UUID]protectedRow {
	t.Helper()
	refTexts := make([]string, 0, len(refs))
	for _, ref := range refs {
		refTexts = append(refTexts, ref.String())
	}
	rows, err := pool.Query(t.Context(), runtimeAuditProtectedValuesQuery, refTexts)
	if err != nil {
		t.Fatalf("pool.Query of audit.pii as audit_redactor: err = %v, want nil", err)
	}
	defer rows.Close()
	values := map[uuid.UUID]protectedRow{}
	for rows.Next() {
		var ref uuid.UUID
		var value protectedRow
		if err := rows.Scan(&ref, &value.Value, &value.Redacted); err != nil {
			t.Fatalf("rows.Scan of audit.pii row: err = %v, want nil", err)
		}
		values[ref] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err() after reading audit.pii = %v, want nil", err)
	}
	return values
}

// requireProtectedValues requires each reference in want to identify an
// unredacted JSON string equal to its expected value.
func requireProtectedValues(t *testing.T, pool *pgxpool.Pool, want map[uuid.UUID]string) {
	t.Helper()
	refs := make([]uuid.UUID, 0, len(want))
	for ref := range want {
		refs = append(refs, ref)
	}
	values := protectedValues(t, pool, refs)
	for ref, text := range want {
		var got string
		value, found := values[ref]
		if !found || value.Redacted || json.Unmarshal(value.Value, &got) != nil || got != text {
			t.Fatalf("values[%s] = %+v, found = %t; want found = true, value.Redacted = false, and value.Value decoding to text", ref, value, found)
		}
	}
}

// requireRedacted requires each reference in refs to identify a redacted
// audit.pii row that does not contain a payload.
func requireRedacted(t *testing.T, pool *pgxpool.Pool, refs []uuid.UUID) {
	t.Helper()
	values := protectedValues(t, pool, refs)
	for _, ref := range refs {
		value, found := values[ref]
		if !found || !value.Redacted || len(value.Value) != 0 {
			t.Fatalf("values[%s] after redaction = %+v, found = %t; want found = true, value.Redacted = true, and len(value.Value) = 0", ref, value, found)
		}
	}
}
