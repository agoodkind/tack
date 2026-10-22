package integration_test

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityMutationCreateRetry checks retries after an issue create
// with an idempotency key. Retries through the service and ApplyMutation with
// a new node ID must return the first issue without creating a second issue.
// A different fingerprint must fail with domain.ErrConflict. The stored
// idempotency record must identify the first issue.
func TestNodeIntegrityMutationCreateRetry(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	input := schemaCreateInput(projectID, projectID, "issue", nil)
	input.IdempotencyKey, input.IdempotencyFingerprint, input.IdempotencySource = "retry-key", "same-request", "mcp"
	first, err := env.NodeSvc.Create(env.Ctx, input)
	if err != nil || first.Existed {
		t.Fatalf("first create = %+v, %v, want a new issue", first, err)
	}
	retry, err := env.NodeSvc.Create(env.Ctx, input)
	if err != nil || !retry.Existed || retry.View.ID != first.View.ID {
		t.Fatalf("service retry = %+v, %v, want existing issue %s", retry, err, first.View.ID)
	}
	different := input
	different.IdempotencyFingerprint = "different-request"
	_, err = env.NodeSvc.Create(env.Ctx, different)
	requireError(t, err, domain.ErrConflict, "Create with a reused key and a different fingerprint")

	newID := uuid.Must(uuid.NewV7())
	receipt, err := env.Stores.Nodes.ApplyMutation(env.Ctx, retryCreatePlan(t, env, projectID, input, newID))
	if err != nil || receipt.NodeID != first.View.ID {
		t.Fatalf("store retry receipt = %+v, %v, want issue %s", receipt, err, first.View.ID)
	}
	if snapshot, _ := mutationSnapshot(t, env, newID); snapshot.Exists {
		t.Fatalf("the store retry created issue %s", newID)
	}
	record, err := env.Stores.Nodes.LookupIdempotencyKey(env.Ctx, env.OrgID, input.IdempotencyKey)
	if err != nil || record == nil || record.NodeID != first.View.ID || record.Fingerprint != "same-request" || record.Source != "mcp" {
		t.Fatalf("idempotency record = %+v, %v, want issue %s", record, err, first.View.ID)
	}
	if issues := listByScope(t, env, "issue", projectID); len(issues) != 1 {
		t.Fatalf("project %s has %d issues, want 1", projectID, len(issues))
	}
}

// TestNodeIntegrityMutationCreateRetryRecordsOneEvent creates an issue with
// an idempotency key under an audit slot. It repeats the create through the
// service, then calls ApplyMutation with a new node ID under a new audit slot.
// It checks that the outbox records exactly one node.create event for the
// issue and does not record an event for the new node ID. It also checks that
// the store retry does not report a commit.
func TestNodeIntegrityMutationCreateRetryRecordsOneEvent(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	input := schemaCreateInput(projectID, projectID, "issue", nil)
	input.IdempotencyKey, input.IdempotencyFingerprint, input.IdempotencySource = "event-key", "same-request", "mcp"
	first, err := env.NodeSvc.Create(auditSlot(env, input.ActorID), input)
	if err != nil || first.Existed {
		t.Fatalf("first create = %+v, %v, want a new issue", first, err)
	}
	if retry, err := env.NodeSvc.Create(auditSlot(env, input.ActorID), input); err != nil || retry.View.ID != first.View.ID {
		t.Fatalf("service retry = %+v, %v, want existing issue %s", retry, err, first.View.ID)
	}

	newID := uuid.Must(uuid.NewV7())
	retryContext := auditSlot(env, input.ActorID)
	receipt, err := env.Stores.Nodes.ApplyMutation(retryContext, retryCreatePlan(t, env, projectID, input, newID))
	if err != nil || receipt.NodeID != first.View.ID || auditintent.Committed(retryContext) {
		t.Fatalf("store retry = %+v, %v, committed %t, want issue %s without a committed event",
			receipt, err, auditintent.Committed(retryContext), first.View.ID)
	}
	outboxEvent(t, env.Stores, first.View.ID, audit.VerbNodeCreate)
	if count := outboxEventCount(t, env, newID); count != 0 {
		t.Fatalf("the outbox records %d events for node %s, want none", count, newID)
	}
}

// TestNodeIntegrityMutationCreateClaimsSharedSequence checks sequence and
// reference assignment for an issue and an epic in one project and a comment
// under the issue. The issue and epic must use numbers 1 and 2 from the same
// project counter and references from the project identifier. The comment type
// does not have a reference template and must not receive a sequence.
func TestNodeIntegrityMutationCreateClaimsSharedSequence(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	epic := createSchemaNode(t, env, projectID, projectID, "epic", nil)
	comment := createSchemaNode(t, env, issue.ID, projectID, "comment", schemaProps("description", quoted("Unnumbered")))

	for reference, created := range map[string]*node.NodeView{"SCHEMA-1": issue, "SCHEMA-2": epic} {
		owner, err := env.Stores.Nodes.LookupReference(env.Ctx, env.OrgID, "reference", reference)
		if err != nil || owner != created.ID {
			t.Fatalf("reference %s owner = %s, %v, want %s %s", reference, owner, err, created.NodeType, created.ID)
		}
	}
	if string(issue.Props["sequence"]) != "1" || string(epic.Props["sequence"]) != "2" {
		t.Fatalf("sequences = %s and %s, want 1 and 2", issue.Props["sequence"], epic.Props["sequence"])
	}
	if _, numbered := storedStoreView(t, env.Stores, comment.ID).Props["sequence"]; numbered {
		t.Fatalf("comment %s received a sequence", comment.ID)
	}
}

// TestNodeIntegrityMutationUpdateKeepsLegacyProperty checks updates to an
// issue with an undeclared legacy property from test storage setup. A service
// update of a declared property must preserve the legacy value. A later
// update that sets the legacy property to JSON null must remove it.
func TestNodeIntegrityMutationUpdateKeepsLegacyProperty(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	stored, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, issue.ID)
	if err != nil || stored == nil {
		t.Fatalf("get issue %s: %v, %v", issue.ID, stored, err)
	}
	stored.Props["parent_epic_title"] = quoted("Legacy epic")
	legacyView := *issue
	legacyView.Props = stored.Props
	if err := env.Stores.Nodes.Set(env.Ctx, stored, &legacyView); err != nil {
		t.Fatalf("store the legacy property on %s: %v", issue.ID, err)
	}

	update := service.UpdateInput{
		NodeID: issue.ID, Name: nil, Props: schemaProps("priority", quoted("high")),
		RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
	}
	if _, err := env.NodeSvc.Update(env.Ctx, update); err != nil {
		t.Fatalf("update priority of %s: %v", issue.ID, err)
	}
	if got := string(storedStoreView(t, env.Stores, issue.ID).Props["parent_epic_title"]); got != `"Legacy epic"` {
		t.Fatalf("parent_epic_title after the priority update = %s, want the legacy value", got)
	}
	update.Props = schemaProps("parent_epic_title", []byte("null"))
	if _, err := env.NodeSvc.Update(env.Ctx, update); err != nil {
		t.Fatalf("remove parent_epic_title of %s: %v", issue.ID, err)
	}
	if _, kept := storedStoreView(t, env.Stores, issue.ID).Props["parent_epic_title"]; kept {
		t.Fatalf("issue %s keeps parent_epic_title after its removal", issue.ID)
	}
}
