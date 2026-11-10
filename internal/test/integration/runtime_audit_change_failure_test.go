package integration_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// changeEventFailure is the validation error of a change event under
// futureOperation.
const changeEventFailure = "completion time cannot precede the start time"

// productRecordTimeout bounds the read of the node and relationship records.
const productRecordTimeout = 5 * time.Second

// productRecordFamilies lists the FoundationDB key families that store
// nodes, their indexes, and relationships.
var productRecordFamilies = []string{
	"node_instance", "node_view", "node_resolve", "node_by_property",
	"node_reference", "node_reference_owned", "relationship", "relationship_reverse",
}

// TestRuntimeAuditRepairFailedChange previews a parent repair of an issue
// with a legacy parent_epic_title through ops.RepairConsole, the core of
// `ops repair apply`. It applies the preview under futureOperation. Each
// change event of that operation fails validation inside the commit
// transaction. The apply must fail and leave every node, relationship, and
// FoundationDB outbox entry unchanged.
func TestRuntimeAuditRepairFailedChange(t *testing.T) {
	fixture := newRepairConsoleFixture(t)
	epic := createSchemaNode(t, fixture.env, fixture.projectID, fixture.projectID, "epic", nil)
	issue := fixture.legacyIssue(t, map[string]json.RawMessage{"parent_epic_title": quoted(epic.Name)})
	input := ops.RepairPreviewInput{
		Class: ops.RepairClassParentReference, NodeID: issue.ID, Profile: parentProfile("epic", "name", "parent_epic_title"),
	}
	preview, err := fixture.console.Preview(fixture.env.Ctx, input)
	if err != nil || !preview.CanApply {
		t.Fatalf("Preview = %+v, %v, want an applicable repair", preview, err)
	}
	before := productRecords(t)
	mark := outboxTail(t, fixture.env)

	_, err = fixture.console.Apply(withFutureOperation(t, fixture.env.Ctx), ops.RepairApplyInput{
		ActorID: uuid.New(), Class: input.Class, ConfirmationToken: preview.ConfirmationToken, NodeID: issue.ID, Profile: input.Profile,
	})
	requireChangeEventFailure(t, "RepairConsole.Apply", err)
	requireFoundationDBUnchanged(t, fixture.env, before, mark)
}

// TestRuntimeAuditBackfillFailedChange deletes a project with the single-node
// store delete, which leaves its epic and states without a parent. It runs
// ops.RunOrphanNodeBackfill, the core of `ops backfill once-orphan-nodes`,
// under futureOperation. Each change event of that operation fails
// validation inside the commit transaction. The backfill must fail and leave
// every node, relationship, and FoundationDB outbox entry unchanged.
func TestRuntimeAuditBackfillFailedChange(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	epic := createSchemaNode(t, env, projectID, projectID, "epic", nil)
	createSchemaNode(t, env, epic.ID, projectID, "issue", nil)
	if err := env.Stores.Nodes.Delete(env.Ctx, env.OrgID, projectID); err != nil {
		t.Fatalf("delete project %s without its descendants: %v", projectID, err)
	}
	principal := sqlAuditPrincipal()
	ctx := audit.WithOpID(audit.WithOperatorPrincipal(t.Context(), principal), uuid.Must(uuid.NewV7()))
	planned, err := ops.RunOrphanNodeBackfill(ctx, env.Stores.NodeDeleter, env.Stores.Relationships, env.NodeSvc, true)
	if err != nil || planned.Orphans == 0 {
		t.Fatalf("dry-run orphan backfill = %+v, %v, want at least one orphan", planned, err)
	}
	before := productRecords(t)
	mark := outboxTail(t, env)

	_, err = ops.RunOrphanNodeBackfill(withFutureOperation(t, ctx), env.Stores.NodeDeleter, env.Stores.Relationships, env.NodeSvc, false)
	requireChangeEventFailure(t, "RunOrphanNodeBackfill", err)
	requireFoundationDBUnchanged(t, env, before, mark)
}

// withFutureOperation attaches futureOperation to ctx.
func withFutureOperation(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	attached, err := audit.WithMutationContext(ctx, futureOperation(t))
	if err != nil {
		t.Fatalf("attach the failing operation: %v", err)
	}
	return attached
}

// requireChangeEventFailure requires err to report the change event
// validation failure of futureOperation.
func requireChangeEventFailure(t *testing.T, step string, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), changeEventFailure) {
		t.Fatalf("%s error = %v, want an error that contains %q", step, err, changeEventFailure)
	}
}

// requireFoundationDBUnchanged requires the node and relationship records to
// equal before and the FoundationDB outbox to hold zero entries after mark.
func requireFoundationDBUnchanged(t *testing.T, env *integration.TestEnv, before map[string]string, mark []byte) {
	t.Helper()
	after := productRecords(t)
	changed := 0
	for key, value := range before {
		if stored, ok := after[key]; !ok || stored != value {
			changed++
		}
	}
	for key := range after {
		if _, ok := before[key]; !ok {
			changed++
		}
	}
	if changed != 0 {
		t.Fatalf("%d node and relationship records changed after the failed audit write; %d before, %d after", changed, len(before), len(after))
	}
	if events := outboxEventsAfter(t, env, mark); len(events) != 0 {
		t.Fatalf("the failed audit write added outbox events %+v", events)
	}
}

// productRecords reads every key and value of productRecordFamilies under the
// active test prefix in one transaction.
func productRecords(t *testing.T) map[string]string {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), productRecordTimeout)
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	records := map[string]string{}
	_, err = database.ReadTransact(func(transaction fdb.ReadTransaction) (any, error) {
		clear(records)
		for _, family := range productRecordFamilies {
			prefix := append(append([]byte{}, fdbadapter.TestPrefixRange()...), tuple.Tuple{family}.Pack()...)
			keyRange, err := fdb.PrefixRange(prefix)
			if err != nil {
				return nil, err
			}
			pairs, err := transaction.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
			if err != nil {
				return nil, err
			}
			for _, pair := range pairs {
				records[string(pair.Key)] = string(pair.Value)
			}
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("read the node and relationship records: %v", err)
	}
	return records
}
