package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/internal/testenv/opsoutbox"
)

type fixedUsers struct {
	byEmail map[string]*user.User
}

func (f fixedUsers) GetByEmail(_ context.Context, email string) (*user.User, error) {
	found, ok := f.byEmail[email]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return found, nil
}

type fixedMembers struct {
	orgs map[uuid.UUID][]uuid.UUID
}

func (f fixedMembers) ListOrgIDsForUser(_ context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	return f.orgs[userID], nil
}

type fixedResolver struct {
	records map[uuid.UUID]*node.NodeResolve
}

func (f fixedResolver) Resolve(_ context.Context, nodeID uuid.UUID) (*node.NodeResolve, error) {
	found, ok := f.records[nodeID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return found, nil
}

// stagingCreator stages the row the way the node service does, commits it the
// way the store does, and keeps each create input, staged row, and actor.
type stagingCreator struct {
	calls  []service.CreateInput
	staged []audit.Event
	actors []uuid.UUID
}

func (c *stagingCreator) Create(ctx context.Context, in service.CreateInput) (*service.CreateResult, error) {
	c.calls = append(c.calls, in)
	actor, _ := auth.UserID(ctx)
	c.actors = append(c.actors, actor)
	id := uuid.New()
	if err := audit.StageStateChange(ctx, audit.VerbNodeCreate, audit.Entity{Type: "node", NodeType: in.NodeTypeKey, ID: id, Identifier: "", Name: in.Name}); err != nil {
		return nil, err
	}
	payload, _ := auditintent.Pending(ctx)
	var event audit.Event
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, err
	}
	c.staged = append(c.staged, event)
	auditintent.Commit(ctx)
	return &service.CreateResult{View: &node.NodeView{ID: id, OrgID: uuid.Nil, NodeType: in.NodeTypeKey, Name: in.Name}, Existed: false}, nil
}

// actAsFixture wires the command to the real SQL outbox on the test ledger
// and to the identity source the command line resolves from operator flags.
type actAsFixture struct {
	deps     actAsDeps
	pool     *pgxpool.Pool
	creator  *stagingCreator
	user     *user.User
	orgID    uuid.UUID
	parentID uuid.UUID
}

func newActAsFixture(t *testing.T, operatorFlags []string) actAsFixture {
	t.Helper()
	pool := testenv.LedgerPool(t, testenv.Ledger(t))
	target := &user.User{ID: uuid.New(), Email: "member@example.test", DisplayName: "Member"}
	orgID := uuid.New()
	parentID := uuid.New()
	creator := &stagingCreator{}
	return actAsFixture{
		deps: actAsDeps{
			outbox: audit.NewPoolOutbox(pool), identity: flagOperatorSource(t, operatorFlags),
			users:   fixedUsers{byEmail: map[string]*user.User{target.Email: target}},
			members: fixedMembers{orgs: map[uuid.UUID][]uuid.UUID{target.ID: {orgID}}},
			reader:  fixedResolver{records: map[uuid.UUID]*node.NodeResolve{parentID: {OrgID: orgID, NodeType: "project"}}},
			nodes:   creator,
		},
		pool: pool, creator: creator, user: target, orgID: orgID, parentID: parentID,
	}
}

func actAsInput(f actAsFixture, email, reason string) actAsCreateInput {
	return actAsCreateInput{Email: email, Reason: reason, ParentID: f.parentID.String(), NodeType: "issue", Name: "Fix the board"}
}

// actAsGrantRows reads the grant rows naming the fixture's user from the
// operator outbox.
func actAsGrantRows(t *testing.T, f actAsFixture) []audit.Event {
	t.Helper()
	filter := opsoutbox.Filter{Verb: audit.VerbOpsActAsGrant, Path: []string{"entity", "id"}, Value: f.user.ID.String()}
	deleteOutboxRowsAfterTest(t, f.pool, filter)
	return opsoutbox.Events(t, f.pool, filter)
}

// TestActAsCreateWritesAsTheUserWithTheOperatorOnTheRow requires the write to
// run as the user, and the user's row to store the operator and the grant id
// of the grant row.
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
	if len(f.creator.calls) != 1 || f.creator.calls[0].ActorID != f.user.ID || f.creator.actors[0] != f.user.ID {
		t.Fatalf("create = %+v as %v, want one create as the user", f.creator.calls, f.creator.actors)
	}
	staged := f.creator.staged[0]
	if staged.Actor.ID != f.user.ID || staged.Context.OrgID != f.orgID || staged.Context.Source != audit.SourceOperator {
		t.Fatalf("staged row = %+v, want the user as actor on the org under the operator source", staged)
	}
	var extra struct {
		ActAs audit.ActProvenance `json:"act_as"`
	}
	if err := json.Unmarshal(staged.Extra, &extra); err != nil {
		t.Fatalf("decode the row's extra: %v", err)
	}
	if extra.ActAs.OperatorID != operatorID || extra.ActAs.GrantID != grant.GrantID || extra.ActAs.Reason != "board stuck after a rename" {
		t.Fatalf("row extra = %+v, want the operator, grant %s, and the reason", extra.ActAs, grant.GrantID)
	}
	if !strings.Contains(sink.buf.String(), `"row_committed":true`) {
		t.Fatalf("report = %s, want row_committed true", sink.buf.String())
	}
}

// TestActAsCreateRefusesBeforeRecordingAnything requires a missing reason, an
// unknown user, and a user outside the parent's org each to refuse before the
// command records a grant row or attempts a write.
func TestActAsCreateRefusesBeforeRecordingAnything(t *testing.T) {
	stranger := uuid.New()
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
			if tc.outside {
				f.deps.members = fixedMembers{orgs: map[uuid.UUID][]uuid.UUID{f.user.ID: {stranger}}}
			}
			err := runActAsCreate(t.Context(), f.deps, actAsInput(f, tc.email(f), tc.reason), &bufferSink{buf: &bytes.Buffer{}}, true)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if rows := actAsGrantRows(t, f); len(rows) != 0 || len(f.creator.calls) != 0 {
				t.Fatalf("refusal recorded %d grant rows and made %d writes, want none", len(rows), len(f.creator.calls))
			}
		})
	}
}
