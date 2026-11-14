package servercli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

// TestAuditHostCommandsReadAndRedactWithinOneOrg runs audit get, query, and
// redact-actor through Cobra against the test ledger. Separate logins inherit
// only audit_reader or audit_redactor privileges to enforce deployed permissions.
func TestAuditHostCommandsReadAndRedactWithinOneOrg(t *testing.T) {
	adminDSN := testenv.Ledger(t)
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
		In:  strings.NewReader(""),
		Out: output,
		Err: &bytes.Buffer{},
	}
	run := func(args ...string) string {
		t.Helper()
		output.Reset()
		factory.SetAuditOutbox(outbox)
		factory.SetAuditReads(audit.NewPoolReadLifecycle(admin))
		root := buildRoot(factory)
		root.SetContext(ctx)
		root.SetArgs(
			append(
				[]string{
					"--operator-id",
					"019dd226-440e-729a-a442-281aaf73ca30",
					"--operator-email",
					"operator@example.com",
				},
				args...,
			),
		)
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, output.String())
		}

		return output.String()
	}

	// audit get: the command returns exactly one row for a stored event.
	var got auditGetResult
	decodeCommandOutput(t, run("audit", "get", "--event-id", eventA1.String(), "--execute"), &got)
	if got.Row.EventID != eventA1 || got.Row.OrgID != orgA || got.Row.PIIRef == nil {
		t.Fatalf(
			"audit get returned %+v, want event %s of org %s with a pii ref",
			got.Row,
			eventA1,
			orgA,
		)
	}

	// Without --execute, the command prints its plan without recording events.
	dry := run("audit", "get", "--event-id", eventA1.String())
	if !strings.Contains(dry, "would run: audit.read") || len(outbox.events) != 0 {
		t.Fatalf(
			"audit get dry run = %q with %d recorded rows, want a plan and no new row",
			dry,
			len(outbox.events),
		)
	}

	// The query must return only the requested organization's rows.
	window := []string{
		"--oldest",
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		"--latest",
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	var queried auditQueryResult
	decodeCommandOutput(
		t,
		run(
			append(
				[]string{
					"audit",
					"query",
					"--org",
					orgA.String(),
					"--actor-id",
					actor.String(),
					"--execute",
				},
				window...,
			)...,
		),
		&queried,
	)
	if queried.RowCount != 2 || len(queried.Rows) != 2 {
		t.Fatalf("audit query returned %d rows, want the org's 2", queried.RowCount)
	}

	for _, row := range queried.Rows {
		if row.OrgID != orgA || (row.EventID != eventA1 && row.EventID != eventA2) {
			t.Fatalf("audit query returned a row from outside the org: %+v", row)
		}
	}

	// A full page includes next_cursor. Subsequent requests must return the
	// remaining rows without overlap or gaps.
	var pageOne auditQueryResult
	decodeCommandOutput(
		t,
		run(
			append(
				[]string{
					"audit",
					"query",
					"--org",
					orgA.String(),
					"--actor-id",
					actor.String(),
					"--limit",
					"1",
					"--execute",
				},
				window...,
			)...,
		),
		&pageOne,
	)
	if pageOne.RowCount != 1 || pageOne.NextCursor == "" {
		t.Fatalf(
			"page one = %d rows with cursor %q, want 1 row and a cursor",
			pageOne.RowCount,
			pageOne.NextCursor,
		)
	}

	var pageTwo auditQueryResult
	decodeCommandOutput(
		t,
		run(
			append(
				[]string{
					"audit",
					"query",
					"--org",
					orgA.String(),
					"--actor-id",
					actor.String(),
					"--limit",
					"1",
					"--cursor",
					pageOne.NextCursor,
					"--execute",
				},
				window...,
			)...,
		),
		&pageTwo,
	)
	if pageTwo.RowCount != 1 || pageTwo.Rows[0].EventID == pageOne.Rows[0].EventID {
		t.Fatalf(
			"page two = %d rows, first %s, want the other event",
			pageTwo.RowCount,
			pageTwo.Rows[0].EventID,
		)
	}

	seen := map[uuid.UUID]bool{pageOne.Rows[0].EventID: true, pageTwo.Rows[0].EventID: true}
	if !seen[eventA1] || !seen[eventA2] {
		t.Fatalf("the two pages did not cover both events: %v", seen)
	}

	// Without --execute, redact-actor reports the two org rows without
	// deleting PII or recording mutation events.
	var planned auditRedactActorResult
	decodeCommandOutput(
		t,
		run("audit", "redact-actor", "--org", orgA.String(), "--actor-id", actor.String()),
		&planned,
	)
	if !planned.DryRun || planned.PIIRefCount != 2 || planned.Unredacted != 2 ||
		planned.Redacted != 0 {
		t.Fatalf("redact plan = %+v, want dry run over 2 unredacted refs", planned)
	}

	if n := unredactedCount(t, admin, eventA1, eventA2, eventB); n != 3 {
		t.Fatalf("pii rows still containing a payload after the plan = %d, want 3", n)
	}

	if len(outbox.events) != 0 {
		t.Fatalf("redact plan changed the recorded rows: %d, want 0", len(outbox.events))
	}

	// redact-actor --execute: erases exactly the org's two refs, leaves the
	// other org's ref, and records the intent and outcome pair.
	var applied auditRedactActorResult
	decodeCommandOutput(
		t,
		run(
			"audit",
			"redact-actor",
			"--org",
			orgA.String(),
			"--actor-id",
			actor.String(),
			"--execute",
		),
		&applied,
	)
	if applied.DryRun || applied.PIIRefCount != 2 || applied.Unredacted != 2 ||
		applied.Redacted != 2 {
		t.Fatalf("redact result = %+v, want 2 of 2 erased", applied)
	}

	if n := unredactedCount(t, admin, eventA1, eventA2); n != 0 {
		t.Fatalf("org rows still containing a payload = %d, want 0", n)
	}

	if n := unredactedCount(t, admin, eventB); n != 1 {
		t.Fatalf(
			"other org's row still containing a payload = %d, want 1: redaction changed an unrelated organization",
			n,
		)
	}

	if len(outbox.events) != 2 ||
		outbox.events[0].Verb != string(
			audit.VerbAuditPIIRedacted,
		) || outbox.events[0].Outcome != audit.OutcomePending ||
		outbox.events[1].Verb != string(
			audit.VerbAuditPIIRedacted,
		) || outbox.events[1].Outcome != audit.OutcomeOK {
		t.Fatalf(
			"redact recorded %+v, want an audit.pii_redacted intent and outcome",
			outbox.events,
		)
	}

	// A rerun must find the references without deleting more PII.
	var rerun auditRedactActorResult
	decodeCommandOutput(
		t,
		run(
			"audit",
			"redact-actor",
			"--org",
			orgA.String(),
			"--actor-id",
			actor.String(),
			"--execute",
		),
		&rerun,
	)
	if rerun.PIIRefCount != 2 || rerun.Unredacted != 0 || rerun.Redacted != 0 {
		t.Fatalf("redact rerun = %+v, want 2 refs without further PII deletion", rerun)
	}
}
