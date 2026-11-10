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

// TestActAsCreateRecordsTheAgentSessionAndAccountableOperator runs act-as with
// a service agent and an operator supplied through flags. It checks that the
// grant actor records the service and session. It also checks that the grant
// extra records the session and accountable operator with the grant reason.
func TestActAsCreateRecordsTheAgentSessionAndAccountableOperator(t *testing.T) {
	f := newActAsFixture(t, []string{
		"--operator-service", actAsAgentService, "--operator-session", actAsAgentSession,
		"--operator-id", testOperatorID, "--operator-email", testOperatorEmail,
	})
	reason := "agent fixes the board " + uuid.NewString()[:8]
	sink := &bufferSink{buf: &bytes.Buffer{}}
	if err := runActAsCreate(t.Context(), f.deps, actAsInput(f, f.user.Email, reason), sink, true); err != nil {
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
	requireAgentUserRow(t, f, actAsCreatedNode(t, f, sink.buf).ID, grant.GrantID, reason)
}

// TestActAsCreateRefusesAServiceWithoutAnAccountableOperator checks that act-as
// rejects a service without an accountable operator before recording a grant or
// creating a node.
func TestActAsCreateRefusesAServiceWithoutAnAccountableOperator(t *testing.T) {
	f := newActAsFixture(t, []string{"--operator-service", actAsAgentService})
	err := runActAsCreate(t.Context(), f.deps, actAsInput(f, f.user.Email, "service alone"), &bufferSink{buf: &bytes.Buffer{}}, true)
	if err == nil || !strings.Contains(err.Error(), "requires --operator-id and --operator-email") {
		t.Fatalf("act-as by a service alone = %v, want the accountable operator refusal", err)
	}
	requireNoActAsWrite(t, f)
}

// requireAgentUserRow checks that the outbox event for the user's node records
// the accountable operator and the agent service, ID, and session.
func requireAgentUserRow(t *testing.T, f actAsFixture, nodeID, grantID uuid.UUID, reason string) {
	t.Helper()
	got := actAsRowProvenance(t, actAsUserRow(t, f, nodeID))
	want := audit.ActProvenance{
		OperatorID: uuid.MustParse(testOperatorID), OperatorEmail: testOperatorEmail, GrantID: grantID, Reason: reason,
		AgentName: actAsAgentService, AgentID: cli.ServiceActorID(actAsAgentService), AgentSessionID: actAsAgentSession,
	}
	if got != want {
		t.Fatalf("user row provenance = %+v, want %+v", got, want)
	}
}
