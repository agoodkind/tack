package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/internal/testenv/opsoutbox"
)

// actAsMemberRole is the org_members role of the seeded user.
const actAsMemberRole = 15

// actAsFixture wires the command to the dependencies the command factory
// builds: the SQL outbox, user repository, and org member repository on the
// test ledger, and the node reader and node service on the FoundationDB
// stores. The user is a member of the project's org and not of the outside
// org.
type actAsFixture struct {
	deps            actAsDeps
	pool            *pgxpool.Pool
	stores          *fdbadapter.Stores
	user            *user.User
	orgID           uuid.UUID
	parentID        uuid.UUID
	outsideParentID uuid.UUID
}

func newActAsFixture(t *testing.T, operatorFlags []string) actAsFixture {
	t.Helper()
	pool := testenv.LedgerPool(t, testenv.Ledger(t))
	stores := actAsStores(t)
	nodes := service.NewNodeService(
		stores.Nodes, stores.Views, stores.NodeTypes, stores.PropertyDefs,
		stores.Relationships, stores.NodeDeleter,
	)
	orgID := writeActAsOrg(t, stores, "act-as-member-org")
	outsideOrgID := writeActAsOrg(t, stores, "act-as-outside-org")
	workspaceID := createActAsScope(t, nodes, "workspace", "Main", orgID)
	projectID := createActAsScope(t, nodes, "project", "Board", workspaceID)
	users := postgres.NewUserRepo(pool)
	members := postgres.NewOrgMemberRepo(pool)
	member := seedActAsMember(t, pool, users, members, orgID)
	return actAsFixture{
		deps: actAsDeps{
			outbox: audit.NewPoolOutbox(pool), identity: flagOperatorSource(t, operatorFlags),
			users: users, members: members, reader: stores.Views, nodes: nodes,
		},
		pool: pool, stores: stores, user: member, orgID: orgID,
		parentID: projectID, outsideParentID: outsideOrgID,
	}
}

// seedActAsMember creates a user with a unique email and adds it to orgID
// through the production repositories, and deletes the user and its
// membership when the test ends.
func seedActAsMember(t *testing.T, pool *pgxpool.Pool, users *postgres.UserRepo, members *postgres.OrgMemberRepo, orgID uuid.UUID) *user.User {
	t.Helper()
	created, err := users.Create(t.Context(), &user.User{
		ID: uuid.Must(uuid.NewV7()), Email: "act-as-" + uuid.NewString()[:8] + "@example.test",
		DisplayName: "Member", AvatarURL: nil,
	})
	if err != nil {
		t.Fatalf("create the member user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM users WHERE id = $1`, created.ID)
	})
	membership := &org.Member{ID: uuid.Nil, OrgID: orgID, UserID: created.ID, Role: actAsMemberRole}
	if err := members.AddMember(t.Context(), membership); err != nil {
		t.Fatalf("add the member to org %s: %v", orgID, err)
	}
	return created
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

// actAsCreatedNode reads the node ID from the command's report and reads the
// node back through the node reader.
func actAsCreatedNode(t *testing.T, f actAsFixture, report *bytes.Buffer) *node.NodeView {
	t.Helper()
	var result actAsCreateResult
	if err := json.Unmarshal(report.Bytes(), &result); err != nil {
		t.Fatalf("decode the report: %v\n%s", err, report.String())
	}
	nodeID, err := uuid.Parse(result.NodeID)
	if err != nil {
		t.Fatalf("report node_id %q: %v", result.NodeID, err)
	}
	view, err := f.stores.Views.Get(t.Context(), nodeID)
	if err != nil || view == nil {
		t.Fatalf("read node %s back: view %+v, err %v", nodeID, view, err)
	}
	return view
}

// actAsUserRow returns the one node.create row for nodeID that the store
// committed to the FoundationDB outbox with the node.
func actAsUserRow(t *testing.T, f actAsFixture, nodeID uuid.UUID) audit.Event {
	t.Helper()
	var matches []audit.Event
	for _, row := range actAsOutboxRows(t, f.stores) {
		if row.Verb == string(audit.VerbNodeCreate) && row.Entity.ID == nodeID {
			matches = append(matches, row)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("node.create rows for %s = %+v, want one", nodeID, matches)
	}
	return matches[0]
}

// actAsRowProvenance decodes the act_as provenance from a user row's extra.
func actAsRowProvenance(t *testing.T, row audit.Event) audit.ActProvenance {
	t.Helper()
	var extra struct {
		ActAs audit.ActProvenance `json:"act_as"`
	}
	if err := json.Unmarshal(row.Extra, &extra); err != nil {
		t.Fatalf("decode the user row's extra: %v", err)
	}
	return extra.ActAs
}

// requireNoActAsWrite requires the command to have recorded no grant row,
// committed no outbox row, and created no issue in the member org.
func requireNoActAsWrite(t *testing.T, f actAsFixture) {
	t.Helper()
	grants := actAsGrantRows(t, f)
	userRows := actAsOutboxRows(t, f.stores)
	issues := actAsIssues(t, f.stores, f.orgID)
	if len(grants) != 0 || len(userRows) != 0 || len(issues) != 0 {
		t.Fatalf("recorded %d grant rows and %d user rows and created %d issues, want none",
			len(grants), len(userRows), len(issues))
	}
}
