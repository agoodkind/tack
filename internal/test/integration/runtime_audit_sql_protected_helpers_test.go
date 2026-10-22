package integration_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

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

// openRedactor opens a redactor with an audit_redactor login and registers
// its connection cleanup.
func openRedactor(t *testing.T, stack runtimeAuditStack) *audit.Redactor {
	t.Helper()
	redactor, err := audit.NewRedactor(t.Context(), roleLoginDSN(t, stack.admin, testenv.Ledger(t), auditRedactorRole))
	if err != nil {
		t.Fatalf("audit.NewRedactor as audit_redactor: error = %v, want nil", err)
	}
	t.Cleanup(redactor.Close)
	return redactor
}

// requireProtectedValues requires each reference in want to identify an
// unredacted JSON string equal to its expected value.
func requireProtectedValues(t *testing.T, redactor *audit.Redactor, want map[uuid.UUID]string) {
	t.Helper()
	refs := make([]uuid.UUID, 0, len(want))
	for ref := range want {
		refs = append(refs, ref)
	}
	values, err := redactor.ProtectedValues(t.Context(), refs)
	if err != nil {
		t.Fatalf("ProtectedValues error = %v, want nil", err)
	}
	for ref, text := range want {
		var got string
		value, found := values[ref]
		if !found || value.Redacted || json.Unmarshal(value.Value, &got) != nil || got != text {
			t.Fatalf("values[%s] = %+v, found = %t; want found = true, value.Redacted = false, and value.Value decoding to text", ref, value, found)
		}
	}
}

// requireRedacted requires each reference to identify a redacted audit.pii
// row that does not contain a payload.
func requireRedacted(t *testing.T, redactor *audit.Redactor, refs []uuid.UUID) {
	t.Helper()
	values, err := redactor.ProtectedValues(t.Context(), refs)
	if err != nil {
		t.Fatalf("ProtectedValues error = %v, want nil", err)
	}
	for _, ref := range refs {
		value, found := values[ref]
		if !found || !value.Redacted || len(value.Value) != 0 {
			t.Fatalf("values[%s] after redaction = %+v, found = %t; want found = true, value.Redacted = true, and len(value.Value) = 0", ref, value, found)
		}
	}
}

// requireExportWithout exports orgID between oldest and latest, verifies the
// bundle, and fails if any bundle file contains a value in personal.
func requireExportWithout(t *testing.T, stack runtimeAuditStack, orgID uuid.UUID, oldest, latest time.Time, personal []string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey error = %v, want nil", err)
	}
	dir := t.TempDir()
	filter := audit.QueryFilter{
		OrgID: orgID, Oldest: oldest, Latest: latest, Action: "", ActorID: uuid.Nil, EntityID: uuid.Nil,
		RequestID: "", TraceID: "", Limit: 0,
	}
	if _, err := audit.Export(t.Context(), stack.reader, private, audit.KeyIdentifier(public), filter, dir); err != nil {
		t.Fatalf("Export for orgID %s: error = %v, want nil", orgID, err)
	}
	report, err := audit.VerifyBundle(dir, public)
	if err != nil {
		t.Fatalf("VerifyBundle for orgID %s: error = %v, want nil", orgID, err)
	}
	if report.RowsScanned == 0 || report.Err() != nil {
		t.Fatalf("VerifyBundle for orgID %s: report.RowsScanned = %d, report.Err() = %v; want report.RowsScanned > 0 and report.Err() = nil", orgID, report.RowsScanned, report.Err())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("os.ReadDir error = %v, want nil", err)
	}
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("os.ReadFile for entry.Name() %s: error = %v, want nil", entry.Name(), err)
		}
		for _, value := range personal {
			if strings.Contains(string(content), value) {
				t.Fatalf("export file %s contains a value in personal, want strings.Contains(string(content), value) = false for every value in personal", entry.Name())
			}
		}
	}
}
