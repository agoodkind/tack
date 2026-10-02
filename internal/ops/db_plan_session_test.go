package ops

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// TestDBPlanAcceptsAnotherSessionOfTheOpener opens a plan in one agent
// session, then runs a listed statement and closes the plan from a second
// session of the same agent service for the same accountable operator. Both
// commands succeed. The statement rows and the close row on the audit topic
// record the second session in the actor and in the extra payload.
func TestDBPlanAcceptsAnotherSessionOfTheOpener(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	pipeline.startConsumer(t, ledgerDSN)
	pipeline.startRelay(t, pool)
	const firstSession, secondSession = "session-plan-k", "session-plan-k-second"
	opener := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags(firstSession)))
	second := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags(secondSession)))
	reason := "plan test k " + uuid.NewString()[:8]
	opened, err := openPlan(t, opener, writePlanFile(t, "select 1"), reason, "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	deleteOutboxRowsAfterTest(t, pool, planRowsFilter(opened.PlanID))

	if _, err := runPlanned(t, second, opened.PlanID, "select 1", reason); err != nil {
		t.Fatalf("planned statement from the second session: %v", err)
	}
	if _, err := closePlan(t, second, opened.PlanID, "postcheck k"); err != nil {
		t.Fatalf("plan close from the second session: %v", err)
	}
	mailWithSubject(t, requireMailCount(t, mail, 2), "plan "+opened.PlanID+" closed")

	events := pipeline.planTopicEvents(t, opened.PlanID, 4)
	if kinds := planRowKinds(events); kinds["ops.db_plan_open ok"] != 1 || kinds["ops.db_break_glass pending"] != 1 ||
		kinds["ops.db_break_glass ok"] != 1 || kinds["ops.db_plan_close ok"] != 1 {
		t.Fatalf("plan rows on the audit topic = %v, want the open, pending, ok, and close rows", kinds)
	}
	for _, event := range events {
		want := secondSession
		if event.Verb == string(audit.VerbOpsDBPlanOpen) {
			want = firstSession
		}
		if session := planRowExtraSession(t, event); event.Actor.SessionID != want || session != want {
			t.Fatalf("%s %s row records actor session %q and extra session %q, want %q",
				event.Verb, event.Outcome, event.Actor.SessionID, session, want)
		}
	}
}

// planRowExtraSession returns the session in the extra payload of a plan row:
// extra.session_id on a statement row and extra.principal.session_id on an
// open or close row.
func planRowExtraSession(t *testing.T, event audit.Event) string {
	t.Helper()
	if event.Verb == string(audit.VerbOpsDBBreakGlass) {
		return breakGlassExtra(t, event).SessionID
	}
	var extra dbPlanExtra
	if err := json.Unmarshal(event.Extra, &extra); err != nil {
		t.Fatalf("decode the extra payload of plan row %s: %v", event.EventID, err)
	}
	return extra.Principal.SessionID
}

// planTopicEvents reads the pipeline topic from its start until it has want
// distinct events with planID in extra.plan_id, and returns them in topic
// order. The relay sends the actor session to the topic; audit.events does
// not store it.
func (p planPipeline) planTopicEvents(t *testing.T, planID string, want int) []audit.Event {
	t.Helper()
	client, err := kgo.NewClient(kgo.SeedBrokers(audit.SplitBrokers(p.brokers)...),
		kgo.ConsumeTopics(p.topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatalf("open a kafka client to read topic %s: %v", p.topic, err)
	}
	defer client.Close()
	readCtx, cancel := context.WithTimeout(t.Context(), planPipelineDeadline)
	defer cancel()
	seen := map[uuid.UUID]bool{}
	var events []audit.Event
	for len(events) < want {
		fetches := client.PollFetches(readCtx)
		if errs := fetches.Errors(); len(errs) > 0 {
			t.Fatalf("read topic %s: %v; events of plan %s so far: %+v", p.topic, errs, planID, events)
		}
		for _, record := range fetches.Records() {
			var event audit.Event
			if err := json.Unmarshal(record.Value, &event); err != nil {
				t.Fatalf("decode a record of topic %s: %v", p.topic, err)
			}
			var extra dbPlanExtra
			if err := json.Unmarshal(event.Extra, &extra); err != nil || extra.PlanID.String() != planID || seen[event.EventID] {
				continue
			}
			seen[event.EventID] = true
			events = append(events, event)
		}
	}
	return events
}
