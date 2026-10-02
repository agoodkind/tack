package ops

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

const (
	// actAsAgentService and actAsAgentSession identify the test agent.
	actAsAgentService = "claude-test-agent"
	actAsAgentSession = "session-act-as-1"
)

// TestActAsCreateRecordsTheAgentSessionAndAccountableOperator runs act-as as
// a service agent acting for a flag operator. The grant row records the
// service as the actor with its session, and its extra stores the session and
// the accountable operator with the grant reason.
func TestActAsCreateRecordsTheAgentSessionAndAccountableOperator(t *testing.T) {
	f := newActAsFixture(t, []string{
		"--operator-service", actAsAgentService, "--operator-session", actAsAgentSession,
		"--operator-id", testOperatorID, "--operator-email", testOperatorEmail,
	})
	reason := "agent fixes the board " + uuid.NewString()[:8]
	if err := runActAsCreate(t.Context(), f.deps, actAsInput(f, f.user.Email, reason), &bufferSink{buf: &bytes.Buffer{}}, true); err != nil {
		t.Fatalf("runActAsCreate: %v", err)
	}
	rows := actAsGrantRows(t, f)
	if len(rows) != 1 {
		t.Fatalf("outbox grant rows = %+v, want one", rows)
	}
	actor := rows[0].Actor
	if actor.Type != audit.ActorService || actor.ID != cli.ServiceActorID(actAsAgentService) || actor.SessionID != actAsAgentSession {
		t.Fatalf("grant row actor = %+v, want service %s in session %s", actor, actAsAgentService, actAsAgentSession)
	}
	var grant actAsGrantExtra
	if err := json.Unmarshal(rows[0].Extra, &grant); err != nil {
		t.Fatalf("decode the grant: %v", err)
	}
	want := audit.ActProvenance{
		OperatorID: uuid.MustParse(testOperatorID), OperatorEmail: testOperatorEmail, GrantID: uuid.Nil, Reason: reason,
	}
	if grant.SessionID != actAsAgentSession || grant.OnBehalfOf == nil || *grant.OnBehalfOf != want {
		t.Fatalf("grant extra session = %q on behalf of %+v, want session %s on behalf of %+v",
			grant.SessionID, grant.OnBehalfOf, actAsAgentSession, want)
	}
	requireAgentUserRow(t, f, grant.GrantID, reason)
}

// TestActAsCreateRefusesAServiceWithoutAnAccountableOperator requires act-as
// by a service with no operator to refuse before it records a grant row or
// makes the write.
func TestActAsCreateRefusesAServiceWithoutAnAccountableOperator(t *testing.T) {
	f := newActAsFixture(t, []string{"--operator-service", actAsAgentService})
	err := runActAsCreate(t.Context(), f.deps, actAsInput(f, f.user.Email, "service alone"), &bufferSink{buf: &bytes.Buffer{}}, true)
	if err == nil || !strings.Contains(err.Error(), "requires --operator-id and --operator-email") {
		t.Fatalf("act-as by a service alone = %v, want the accountable operator refusal", err)
	}
	if rows := actAsGrantRows(t, f); len(rows) != 0 || len(f.creator.calls) != 0 {
		t.Fatalf("the refused act-as recorded %d grant rows and made %d writes, want none", len(rows), len(f.creator.calls))
	}
}

// requireAgentUserRow requires the row written as the user to record the
// accountable operator and the agent service, its ID, and its session.
func requireAgentUserRow(t *testing.T, f actAsFixture, grantID uuid.UUID, reason string) {
	t.Helper()
	if len(f.creator.staged) != 1 {
		t.Fatalf("staged user rows = %d, want one", len(f.creator.staged))
	}
	var staged struct {
		ActAs audit.ActProvenance `json:"act_as"`
	}
	if err := json.Unmarshal(f.creator.staged[0].Extra, &staged); err != nil {
		t.Fatalf("decode the user row provenance: %v", err)
	}
	want := audit.ActProvenance{
		OperatorID: uuid.MustParse(testOperatorID), OperatorEmail: testOperatorEmail, GrantID: grantID, Reason: reason,
		AgentName: actAsAgentService, AgentID: cli.ServiceActorID(actAsAgentService), AgentSessionID: actAsAgentSession,
	}
	if staged.ActAs != want {
		t.Fatalf("user row provenance = %+v, want %+v", staged.ActAs, want)
	}
}
