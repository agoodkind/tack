package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
)

type recordingOutbox struct {
	events []audit.Event
}

func (o *recordingOutbox) WriteOutbox(_ context.Context, event audit.Event) error {
	o.events = append(o.events, event)
	return nil
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
