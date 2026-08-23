package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
)

// auditHostTestDSNEnv names a migrated audit database. The test below drives
// the real `audit get`, `audit query`, and `audit redact-actor` commands
// through the cobra root against it, as LOGIN roles that hold exactly the
// audit_reader and audit_redactor grants, so a query the migration does not
// allow fails here the way it fails on a host.
const auditHostTestDSNEnv = "AUDIT_CHAIN_TEST_DSN"

type recordingOutbox struct {
	events []audit.Event
}

func (o *recordingOutbox) WriteOutbox(_ context.Context, event audit.Event) error {
	o.events = append(o.events, event)
	return nil
}

func TestAuditHostCommandsReadAndRedactWithinOneOrg(t *testing.T) {
	adminDSN := os.Getenv(auditHostTestDSNEnv)
	if adminDSN == "" {
		t.Skipf("set %s to a migrated audit DSN to run", auditHostTestDSNEnv)
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	t.Cleanup(admin.Close)
	readerDSN := auditLoginRoleDSN(t, admin, adminDSN, "audit_reader")
	redactorDSN := auditLoginRoleDSN(t, admin, adminDSN, "audit_redactor")

	orgA, orgB, actor := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	eventA1 := recordActorEvent(t, admin, adminDSN, orgA, actor)
	eventA2 := recordActorEvent(t, admin, adminDSN, orgA, actor)
	eventB := recordActorEvent(t, admin, adminDSN, orgB, actor)

	outbox := &recordingOutbox{}
	output := &bytes.Buffer{}
	factory := &cli.Factory{
		Cfg: &config.Config{AuditReaderDSN: readerDSN, AuditRedactorDSN: redactorDSN},
		In:  strings.NewReader(""), Out: output, Err: &bytes.Buffer{},
	}
	run := func(args ...string) string {
		t.Helper()
		output.Reset()
		factory.SetAuditOutbox(outbox)
		root := buildRoot(factory)
		root.SetContext(ctx)
		root.SetArgs(append([]string{
			"--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
			"--operator-email", "operator@example.com",
		}, args...))
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, output.String())
		}
		return output.String()
	}

	// get: one row, recorded as one audit.read access before the read ran.
	var got auditGetResult
	decodeCommandOutput(t, run("audit", "get", "--event-id", eventA1.String(), "--execute"), &got)
	if got.Row.EventID != eventA1 || got.Row.OrgID != orgA || got.Row.PIIRef == nil {
		t.Fatalf("audit get returned %+v, want event %s of org %s with a pii ref", got.Row, eventA1, orgA)
	}
	if len(outbox.events) != 1 || outbox.events[0].Verb != string(audit.VerbAuditRead) {
		t.Fatalf("audit get recorded %+v, want one audit.read row", outbox.events)
	}

	// get without --execute: the choke-point prints the plan and records nothing.
	dry := run("audit", "get", "--event-id", eventA1.String())
	if !strings.Contains(dry, "would run: audit.read") || len(outbox.events) != 1 {
		t.Fatalf("audit get dry run = %q with %d recorded rows, want a plan and no new row", dry, len(outbox.events))
	}

	// query: the org's rows and nothing from the other org.
	window := []string{"--oldest", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "--latest", time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	var queried auditQueryResult
	decodeCommandOutput(t, run(append([]string{"audit", "query", "--org", orgA.String(), "--actor-id", actor.String(), "--execute"}, window...)...), &queried)
	if queried.RowCount != 2 || len(queried.Rows) != 2 {
		t.Fatalf("audit query returned %d rows, want the org's 2", queried.RowCount)
	}
	for _, row := range queried.Rows {
		if row.OrgID != orgA || (row.EventID != eventA1 && row.EventID != eventA2) {
			t.Fatalf("audit query returned a row from outside the org: %+v", row)
		}
	}
	if len(outbox.events) != 2 || outbox.events[1].Verb != string(audit.VerbAuditRead) {
		t.Fatalf("audit query recorded %+v, want a second audit.read row", outbox.events)
	}

	// pagination: a full page carries next_cursor, and the cursor walks the
	// remaining rows without overlap or a gap.
	var pageOne auditQueryResult
	decodeCommandOutput(t, run(append([]string{"audit", "query", "--org", orgA.String(), "--actor-id", actor.String(), "--limit", "1", "--execute"}, window...)...), &pageOne)
	if pageOne.RowCount != 1 || pageOne.NextCursor == "" {
		t.Fatalf("page one = %d rows with cursor %q, want 1 row and a cursor", pageOne.RowCount, pageOne.NextCursor)
	}
	var pageTwo auditQueryResult
	decodeCommandOutput(t, run(append([]string{
		"audit", "query", "--org", orgA.String(), "--actor-id", actor.String(), "--limit", "1",
		"--cursor", pageOne.NextCursor,
		"--execute",
	}, window...)...), &pageTwo)
	if pageTwo.RowCount != 1 || pageTwo.Rows[0].EventID == pageOne.Rows[0].EventID {
		t.Fatalf("page two = %d rows, first %s, want the other event", pageTwo.RowCount, pageTwo.Rows[0].EventID)
	}
	seen := map[uuid.UUID]bool{pageOne.Rows[0].EventID: true, pageTwo.Rows[0].EventID: true}
	if !seen[eventA1] || !seen[eventA2] {
		t.Fatalf("the two pages did not cover both events: %v", seen)
	}

	// redact-actor without --execute: reports the two org rows, erases nothing,
	// records nothing.
	var planned auditRedactActorResult
	decodeCommandOutput(t, run("audit", "redact-actor", "--org", orgA.String(), "--actor-id", actor.String()), &planned)
	if !planned.DryRun || planned.PIIRefCount != 2 || planned.Unredacted != 2 || planned.Redacted != 0 {
		t.Fatalf("redact plan = %+v, want dry run over 2 unredacted refs", planned)
	}
	if n := unredactedCount(t, admin, eventA1, eventA2, eventB); n != 3 {
		t.Fatalf("pii rows still holding a payload after the plan = %d, want 3", n)
	}
	// get + query + two pages have each recorded one audit.read; the plan
	// added nothing.
	if len(outbox.events) != 4 {
		t.Fatalf("redact plan changed the recorded rows: %d, want 4", len(outbox.events))
	}

	// redact-actor --execute: erases exactly the org's two refs, leaves the
	// other org's ref, and records the intent and outcome pair.
	var applied auditRedactActorResult
	decodeCommandOutput(t, run("audit", "redact-actor", "--org", orgA.String(), "--actor-id", actor.String(), "--execute"), &applied)
	if applied.DryRun || applied.PIIRefCount != 2 || applied.Unredacted != 2 || applied.Redacted != 2 {
		t.Fatalf("redact result = %+v, want 2 of 2 erased", applied)
	}
	if n := unredactedCount(t, admin, eventA1, eventA2); n != 0 {
		t.Fatalf("org rows still holding a payload = %d, want 0", n)
	}
	if n := unredactedCount(t, admin, eventB); n != 1 {
		t.Fatalf("other org's row still holding a payload = %d, want 1: the redaction left the org", n)
	}
	if len(outbox.events) != 6 ||
		outbox.events[4].Verb != string(audit.VerbAuditPIIRedacted) || outbox.events[4].Outcome != audit.OutcomePending ||
		outbox.events[5].Verb != string(audit.VerbAuditPIIRedacted) || outbox.events[5].Outcome != audit.OutcomeOK {
		t.Fatalf("redact recorded %+v, want an audit.pii_redacted intent and outcome", outbox.events[4:])
	}

	// A rerun finds the refs and nothing left to erase.
	var rerun auditRedactActorResult
	decodeCommandOutput(t, run("audit", "redact-actor", "--org", orgA.String(), "--actor-id", actor.String(), "--execute"), &rerun)
	if rerun.PIIRefCount != 2 || rerun.Unredacted != 0 || rerun.Redacted != 0 {
		t.Fatalf("redact rerun = %+v, want 2 refs with nothing left to erase", rerun)
	}
}

// auditLoginRoleDSN creates a throwaway LOGIN role that inherits exactly one
// audit base role and returns the admin DSN rewritten to connect as it. The
// throwaway database authenticates loopback connections by trust, so the
// role carries no password.
func auditLoginRoleDSN(t *testing.T, admin *pgxpool.Pool, adminDSN, base string) string {
	t.Helper()
	ctx := context.Background()
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("role suffix: %v", err)
	}
	login := fmt.Sprintf("tack_test_%s_%s", base, hex.EncodeToString(suffix))
	if _, err := admin.Exec(ctx, "CREATE ROLE "+login+" LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE"); err != nil {
		t.Fatalf("create %s: %v", login, err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP ROLE IF EXISTS "+login) })
	if _, err := admin.Exec(ctx, "GRANT "+base+" TO "+login); err != nil {
		t.Fatalf("grant %s to %s: %v", base, login, err)
	}
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parse %s: %v", auditHostTestDSNEnv, err)
	}
	parsed.User = url.User(login)
	return parsed.String()
}

// recordActorEvent writes one event with actor PII through the production
// recorder so the row carries a real pii_ref, and returns its event id.
func recordActorEvent(t *testing.T, admin *pgxpool.Pool, writerDSN string, orgID, actorID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	recorder, err := audit.NewYBRecorder(ctx, writerDSN)
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	defer recorder.Close()
	eventID := uuid.Must(uuid.NewV7())
	err = recorder.Record(ctx, audit.Event{
		Verb:    string(audit.VerbNodeRead),
		EventID: eventID,
		Actor:   audit.Actor{Type: audit.ActorUser, ID: actorID, Email: "erase-me@example.com", Name: "Erase Me"},
		Entity:  audit.Entity{Type: "node", ID: uuid.Must(uuid.NewV7())},
		Context: audit.EventContext{OrgID: orgID, Source: audit.SourceMCP},
		Outcome: audit.OutcomeOK,
	})
	if err != nil {
		t.Fatalf("record event: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, `DELETE FROM audit.pii WHERE pii_ref IN (SELECT pii_ref FROM audit.events WHERE event_id = $1)`, eventID)
		_, _ = admin.Exec(ctx, `DELETE FROM audit.events WHERE event_id = $1`, eventID)
		_, _ = admin.Exec(ctx, `DELETE FROM audit.projected_events WHERE event_id = $1`, eventID)
		_, _ = admin.Exec(ctx, `DELETE FROM audit.chain_heads WHERE org_id = $1`, orgID)
	})
	return eventID
}

// unredactedCount reports how many of the given events' pii rows still hold a
// payload.
func unredactedCount(t *testing.T, admin *pgxpool.Pool, eventIDs ...uuid.UUID) int {
	t.Helper()
	ids := make([]string, 0, len(eventIDs))
	for _, id := range eventIDs {
		ids = append(ids, id.String())
	}
	var count int
	err := admin.QueryRow(context.Background(), `
		SELECT count(*) FROM audit.pii
		 WHERE redacted = false
		   AND pii_ref IN (SELECT pii_ref FROM audit.events WHERE event_id = ANY($1::uuid[]))
	`, ids).Scan(&count)
	if err != nil {
		t.Fatalf("count unredacted: %v", err)
	}
	return count
}

// decodeCommandOutput skips the text lines the sink and the dry-run gate print
// before the JSON body (the trace header, the operator line, the plan line)
// and decodes the body.
func decodeCommandOutput[T auditGetResult | auditQueryResult | auditRedactActorResult](t *testing.T, output string, into *T) {
	t.Helper()
	start := strings.Index(output, "{")
	if start < 0 {
		t.Fatalf("command output carries no JSON body:\n%s", output)
	}
	if err := json.Unmarshal([]byte(output[start:]), into); err != nil {
		t.Fatalf("decode command output: %v\n%s", err, output)
	}
}
