package ops

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// TestActAsCreateWritesAsTheUserWithTheOperatorOnTheRow requires the node to
// be created by the user, and the user's row in the FoundationDB outbox to
// store the operator and the grant id of the grant row.
func TestActAsCreateWritesAsTheUserWithTheOperatorOnTheRow(t *testing.T) {
	f := newActAsFixture(t, humanOperatorFlags())
	sink := &bufferSink{buf: &bytes.Buffer{}}
	if err := runActAsCreate(t.Context(), f.deps, actAsInput(f, f.user.Email, "board stuck after a rename"), sink, true); err != nil {
		t.Fatalf("runActAsCreate: %v", err)
	}
	rows := actAsGrantRows(t, f)
	if len(rows) != 1 {
		t.Fatalf("outbox grant rows = %+v, want one", rows)
	}
	grantRow, operatorID := rows[0], uuid.MustParse(testOperatorID)
	if grantRow.Actor.ID != operatorID || grantRow.Context.OrgID != f.orgID || grantRow.Entity.ID != f.user.ID {
		t.Fatalf("grant row = %+v, want the operator on the user's org naming the user", grantRow)
	}
	var grant actAsGrantExtra
	if err := json.Unmarshal(grantRow.Extra, &grant); err != nil {
		t.Fatalf("decode the grant: %v", err)
	}
	created := actAsCreatedNode(t, f, sink.buf)
	if created.CreatedBy != f.user.ID || created.OrgID != f.orgID || created.NodeType != "issue" || created.Name != "Fix the board" {
		t.Fatalf("created node = %+v, want an issue in the user's org created by the user", created)
	}
	userRow := actAsUserRow(t, f, created.ID)
	if userRow.Actor.ID != f.user.ID || userRow.Context.OrgID != f.orgID || userRow.Context.Source != audit.SourceOperator {
		t.Fatalf("user row = %+v, want the user as actor on the org under the operator source", userRow)
	}
	provenance := actAsRowProvenance(t, userRow)
	if provenance.OperatorID != operatorID || provenance.GrantID != grant.GrantID || provenance.Reason != "board stuck after a rename" {
		t.Fatalf("row extra = %+v, want the operator, grant %s, and the reason", provenance, grant.GrantID)
	}
	if !strings.Contains(sink.buf.String(), `"row_committed":true`) {
		t.Fatalf("report = %s, want row_committed true", sink.buf.String())
	}
}

// TestActAsCreateRefusesBeforeRecordingAnything requires a missing reason, an
// unknown user, and a parent in an org the user is not a member of each to
// refuse before the command records a grant row or makes a write.
func TestActAsCreateRefusesBeforeRecordingAnything(t *testing.T) {
	cases := []struct {
		name    string
		email   func(actAsFixture) string
		reason  string
		outside bool
		want    string
	}{
		{name: "no reason", email: func(f actAsFixture) string { return f.user.Email }, reason: "   ", outside: false, want: "reason is required"},
		{name: "unknown user", email: func(actAsFixture) string { return "nobody@example.test" }, reason: "why", outside: false, want: `no active user "nobody@example.test"`},
		{name: "outside the org", email: func(f actAsFixture) string { return f.user.Email }, reason: "why", outside: true, want: "is not a member of org"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newActAsFixture(t, humanOperatorFlags())
			input := actAsInput(f, tc.email(f), tc.reason)
			if tc.outside {
				input.ParentID = f.outsideParentID.String()
			}
			err := runActAsCreate(t.Context(), f.deps, input, &bufferSink{buf: &bytes.Buffer{}}, true)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			requireNoActAsWrite(t, f)
		})
	}
}
