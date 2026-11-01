//go:build runtimeaudit

package integration_test

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// redactionAuditQuery is the included text of the redaction corpus.
const redactionAuditQuery = "dun causeway redaction audit"

// TestSearchRuntimeAuditOwnerRedactionOnSessionTarget opens a session as owner
// A, and a search by user B sweeps it. The delete row must state the session
// in EntityID and Target.ID, A in Target.OwnerID, and B as actor. audit.pii
// must store each value with subject A. Redaction for B must leave the values
// of A in place. Redaction for A must clear them. The export must verify and
// must omit the raw tokens, sort values, and access keys. The test does not
// require the export to omit query text, because the node.search selection
// stores it as approved.
func TestSearchRuntimeAuditOwnerRedactionOnSessionTarget(t *testing.T) {
	setShortSessions(t)
	fixture := newSessionAuditFixture(t)
	oldest := clock.Now().UTC().Add(-time.Minute)
	fixture.putCorpus(t, redactionAuditQuery, sessionAuditNodes)
	owner, sweeper := fixture.first.Actors[0], fixture.first.Actors[1]
	orgID := fixture.first.OrgID
	page := fixture.mustSearch(t, owner.Token, redactionAuditQuery, "")
	session := sessionOfCursor(t, fixture.parentEdgeFixture, page.Cursor)
	waitUntilTime(t, session.IdleDeadline.Truncate(time.Minute).Add(time.Minute+sessionAuditMargin))
	fixture.mustSearch(t, sweeper.Token, redactionAuditQuery, "")

	rows := fixture.waitForRows(t, orgID, func(current []audit.Row) bool {
		return len(sessionChanges(current, session.ID, audit.VerbSearchSessionDelete, audit.TargetSearchSession)) == 1
	})
	deleted := onlyRow(t, sessionChanges(rows, session.ID, audit.VerbSearchSessionDelete, audit.TargetSearchSession), "the session delete")
	target := deleted.Context.Target
	if deleted.EntityID != session.ID || target.ID != session.ID || target.OwnerID != owner.UserID || deleted.ActorID != sweeper.UserID {
		t.Fatalf("session delete has entity %s, target %+v, actor %s, want session %s, owner %s, actor %s",
			deleted.EntityID, target, deleted.ActorID, session.ID, owner.UserID, sweeper.UserID)
	}
	before := deltaMembers(t, deleted.Delta.Before)
	references := []uuid.UUID{}
	for _, member := range []string{"text", "query_tokens", "sort", "access_keys"} {
		references = append(references, memberReference(t, before, member))
	}
	ownerRefs, err := fixture.redactor.PIIRefsForSubject(t.Context(), orgID, owner.UserID)
	if err != nil {
		t.Fatalf("read the references of the owner: %v", err)
	}
	for _, reference := range references {
		if !slices.Contains(ownerRefs, reference) {
			t.Fatalf("audit.pii does not store reference %s with subject %s", reference, owner.UserID)
		}
	}

	redact := func(actorID uuid.UUID) {
		t.Helper()
		if _, err := audit.RedactActorInOrg(t.Context(), fixture.ledger.stack.reader, fixture.redactor, orgID, actorID); err != nil {
			t.Fatalf("redact actor %s: %v", actorID, err)
		}
	}
	redact(sweeper.UserID)
	fixture.requireProtectedMember(t, before, "text", encodeJSON(t, session.Query.Text))
	fixture.requireProtectedMember(t, before, "query_tokens", session.Snapshot.QueryTokens)
	redact(owner.UserID)
	requireRedacted(t, fixture.redactorPool, references)
	requireExportWithout(t, fixture.ledger.stack, orgID, oldest, clock.Now().UTC().Add(time.Minute), rawSessionValues(t, session))
}

// rawSessionValues returns the raw protected values of session that the
// export must omit.
func rawSessionValues(t *testing.T, session searchdomain.Session) []string {
	t.Helper()
	return []string{
		string(session.Snapshot.QueryTokens), string(session.Sort), string(encodeJSON(t, session.Query.Access.Keys)),
	}
}
