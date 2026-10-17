package integration_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// strictInvalidPriority is a value that the priority select property's options
// do not declare.
const strictInvalidPriority = "strict-test-undeclared-option"

// TestNodeIntegrityStrictMutationInvalidatesEvidence captures evidence before
// updating an issue through the service. It checks that ActivateStrict rejects
// the stale evidence, reports the issue's write fence bucket, and leaves strict
// validation inactive. It then checks that ops verify strict-validation scans
// the records again and activates strict validation.
func TestNodeIntegrityStrictMutationInvalidatesEvidence(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	evidence, err := env.Stores.Nodes.CaptureStrictEvidence(env.Ctx)
	if err != nil {
		t.Fatalf("capture the evidence: %v", err)
	}
	updateStrictPriority(t, env, issue.ID, "high")
	activation := node.StrictActivation{Active: true, ActivatedAt: clock.Now().UTC(), ScanID: uuid.Must(uuid.NewV7()), RecordsValidated: 0}
	change, err := env.Stores.Nodes.ActivateStrict(env.Ctx, evidence, activation)
	bucket := fdbadapter.WriteFenceBucket(env.OrgID, issue.ID)
	if !errors.Is(err, node.ErrStrictEvidenceChanged) || !slices.Contains(change.Buckets, bucket) {
		t.Fatalf("activation on stale evidence = %+v, %v; want ErrStrictEvidenceChanged for bucket %d", change, err, bucket)
	}
	stored, err := env.Stores.Nodes.StrictActivation(env.Ctx)
	if err != nil || stored.Active {
		t.Fatalf("activation after the rejected evidence = %+v, %v; want inactive", stored, err)
	}
	output, err := runStrictValidation(t, true)
	if err != nil || !output.Result.Activated || output.Result.ViolationCount != 0 {
		t.Fatalf("strict validation = %+v, %v; want activation without violations", output.Result, err)
	}
}

// TestNodeIntegrityStrictRejectsInvalidWriteAfterActivation activates strict
// validation before attempting to update an issue's priority to an undeclared
// option through the service. It checks that the update fails with
// [domain.ErrInvalidArgument] and does not change the stored issue. It then
// checks that a valid update succeeds.
func TestNodeIntegrityStrictRejectsInvalidWriteAfterActivation(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	output, err := runStrictValidation(t, true)
	if err != nil || !output.Result.Activated {
		t.Fatalf("strict validation = %+v, %v; want activation", output.Result, err)
	}
	before := storedStoreView(t, env.Stores, issue.ID)
	_, err = env.NodeSvc.Update(env.Ctx, strictPriorityUpdate(issue.ID, strictInvalidPriority))
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("update with priority %q after activation = %v, want an invalid argument", strictInvalidPriority, err)
	}
	after := storedStoreView(t, env.Stores, issue.ID)
	if !reflect.DeepEqual(after.Props, before.Props) || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("the rejected update changed the stored issue: before %+v, after %+v", before, after)
	}
	updateStrictPriority(t, env, issue.ID, "high")
}

func updateStrictPriority(t *testing.T, env *integration.TestEnv, issueID uuid.UUID, priority string) {
	t.Helper()
	if _, err := env.NodeSvc.Update(env.Ctx, strictPriorityUpdate(issueID, priority)); err != nil {
		t.Fatalf("update issue %s priority to %q: %v", issueID, priority, err)
	}
}

func strictPriorityUpdate(issueID uuid.UUID, priority string) service.UpdateInput {
	return service.UpdateInput{
		NodeID: issueID, Name: nil, Props: schemaProps("priority", quoted(priority)),
		RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
	}
}
