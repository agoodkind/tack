package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/service"
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

// stagingCreator stands in for the node service: it stages the row the way
// the service does and commits it the way the store does, and keeps what it
// was asked to create and under which context.
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

type actAsFixture struct {
	deps     actAsDeps
	outbox   *capturedOutbox
	creator  *stagingCreator
	user     *user.User
	orgID    uuid.UUID
	parentID uuid.UUID
	operator audit.OperatorPrincipal
}

func newActAsFixture() actAsFixture {
	target := &user.User{ID: uuid.New(), Email: "member@example.test", DisplayName: "Member"}
	orgID := uuid.New()
	parentID := uuid.New()
	outbox := &capturedOutbox{}
	creator := &stagingCreator{}
	operator := audit.OperatorPrincipal{ID: uuid.New(), Email: "operator@example.test", Name: "Operator", Source: "test"}
	return actAsFixture{
		deps: actAsDeps{
			outbox: outbox, identity: fixedOperator{principal: operator},
			users:   fixedUsers{byEmail: map[string]*user.User{target.Email: target}},
			members: fixedMembers{orgs: map[uuid.UUID][]uuid.UUID{target.ID: {orgID}}},
			reader:  fixedResolver{records: map[uuid.UUID]*node.NodeResolve{parentID: {OrgID: orgID, NodeType: "project"}}},
			nodes:   creator,
		},
		outbox: outbox, creator: creator, user: target, orgID: orgID, parentID: parentID, operator: operator,
	}
}

func actAsInput(f actAsFixture, email, reason string) actAsCreateInput {
	return actAsCreateInput{Email: email, Reason: reason, ParentID: f.parentID.String(), NodeType: "issue", Name: "Fix the board"}
}

// The write lands as the user, and the user's row carries the operator and
// the grant the grant row names.
func TestActAsCreateWritesAsTheUserWithTheOperatorOnTheRow(t *testing.T) {
	f := newActAsFixture()
	sink := &bufferSink{buf: &bytes.Buffer{}}
	if err := runActAsCreate(context.Background(), f.deps, actAsInput(f, f.user.Email, "board stuck after a rename"), sink, true); err != nil {
		t.Fatalf("runActAsCreate: %v", err)
	}
	if len(f.outbox.events) != 1 || f.outbox.events[0].Verb != string(audit.VerbOpsActAsGrant) {
		t.Fatalf("outbox = %+v, want one grant row", f.outbox.events)
	}
	grantRow := f.outbox.events[0]
	if grantRow.Actor.ID != f.operator.ID || grantRow.Context.OrgID != f.orgID || grantRow.Entity.ID != f.user.ID {
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
	if extra.ActAs.OperatorID != f.operator.ID || extra.ActAs.GrantID != grant.GrantID || extra.ActAs.Reason != "board stuck after a rename" {
		t.Fatalf("row extra = %+v, want the operator, grant %s, and the reason", extra.ActAs, grant.GrantID)
	}
	if !strings.Contains(sink.buf.String(), `"row_committed":true`) {
		t.Fatalf("report = %s, want row_committed true", sink.buf.String())
	}
}

// A missing reason, an unknown user, and a user outside the parent's org each
// refuse before any grant is recorded or any write attempted.
func TestActAsCreateRefusesBeforeRecordingAnything(t *testing.T) {
	stranger := uuid.New()
	cases := []struct {
		name  string
		input func(actAsFixture) actAsCreateInput
		want  string
	}{
		{name: "no reason", input: func(f actAsFixture) actAsCreateInput { return actAsInput(f, f.user.Email, "   ") }, want: "reason is required"},
		{name: "unknown user", input: func(f actAsFixture) actAsCreateInput { return actAsInput(f, "nobody@example.test", "why") }, want: `no active user "nobody@example.test"`},
		{name: "outside the org", input: func(f actAsFixture) actAsCreateInput {
			f.deps.members = fixedMembers{orgs: map[uuid.UUID][]uuid.UUID{f.user.ID: {stranger}}}
			return actAsInput(f, f.user.Email, "why")
		}, want: "is not a member of org"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newActAsFixture()
			input := tc.input(f)
			if tc.name == "outside the org" {
				f.deps.members = fixedMembers{orgs: map[uuid.UUID][]uuid.UUID{f.user.ID: {stranger}}}
			}
			err := runActAsCreate(context.Background(), f.deps, input, &bufferSink{buf: &bytes.Buffer{}}, true)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if len(f.outbox.events) != 0 || len(f.creator.calls) != 0 {
				t.Fatalf("refusal recorded %d rows and made %d writes, want none", len(f.outbox.events), len(f.creator.calls))
			}
		})
	}
}

// The dry run names the user and the org and records nothing.
func TestActAsCreateDryRunRecordsNothing(t *testing.T) {
	f := newActAsFixture()
	sink := &bufferSink{buf: &bytes.Buffer{}}
	if err := runActAsCreate(context.Background(), f.deps, actAsInput(f, f.user.Email, "why"), sink, false); err != nil {
		t.Fatalf("runActAsCreate: %v", err)
	}
	if len(f.outbox.events) != 0 || len(f.creator.calls) != 0 {
		t.Fatal("the dry run must record nothing and write nothing")
	}
	if !strings.Contains(sink.buf.String(), f.orgID.String()) || !strings.Contains(sink.buf.String(), `"dry_run":true`) {
		t.Fatalf("report = %s, want the org and dry_run true", sink.buf.String())
	}
}
