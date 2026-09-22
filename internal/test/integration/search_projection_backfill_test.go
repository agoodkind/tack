package integration

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
)

func TestSearchProjectionBackfill(t *testing.T) {
	env := SetupTestEnv(t)
	definitions, err := env.Stores.PropertyDefs.List(env.Ctx, env.OrgID)
	if err != nil {
		t.Fatalf("list property definitions: %v", err)
	}
	if len(definitions) == 0 || definitions[0].Search == nil {
		t.Fatal("seeded property definitions must include search projections")
	}
	definition := *definitions[0]
	projection := *definition.Search
	definition.Search = nil
	if err := env.Stores.PropertyDefs.Set(env.Ctx, &definition); err != nil {
		t.Fatalf("clear projection: %v", err)
	}
	manifest := projectionManifest(t, env.OrgID, definition.ID, projection)
	beforeKeys := snapshotProjectionKeys(t)

	dryRunResult, err := ops.RunSearchProjectionBackfill(env.Ctx, env.Stores.PropertyDefs, strings.NewReader(manifest), true)
	if err != nil {
		t.Fatalf("dry-run backfill: %v", err)
	}
	if dryRunResult.Changed != 1 {
		t.Fatalf("dry-run changed = %d, want 1", dryRunResult.Changed)
	}
	cleared, err := env.Stores.PropertyDefs.Get(env.Ctx, env.OrgID, definition.ID)
	if err != nil {
		t.Fatalf("read after dry-run: %v", err)
	}
	if cleared.Search != nil {
		t.Fatal("dry-run wrote a projection")
	}
	if afterDryRun := snapshotProjectionKeys(t); !reflect.DeepEqual(beforeKeys, afterDryRun) {
		t.Fatal("dry-run changed FoundationDB keys")
	}
	before, err := env.Stores.OpsOutbox.ReadOutboxFrom(env.Ctx, nil, 10)
	if err != nil || len(before) != 0 {
		t.Fatalf("dry-run outbox = %d events, error = %v", len(before), err)
	}

	ctx := audit.WithOperatorPrincipal(env.Ctx, projectionTestPrincipal())
	first, err := ops.RunSearchProjectionBackfill(ctx, env.Stores.PropertyDefs, strings.NewReader(manifest), false)
	if err != nil {
		t.Fatalf("apply backfill: %v", err)
	}
	if first.Changed != 1 || first.Missing != 0 {
		t.Fatalf("first result = %+v", first)
	}
	entries, err := env.Stores.OpsOutbox.ReadOutboxFrom(ctx, nil, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("apply outbox = %d events, error = %v", len(entries), err)
	}
	var recorded audit.Event
	if err := json.Unmarshal(entries[0].Event, &recorded); err != nil {
		t.Fatalf("decode projection audit event: %v", err)
	}
	if recorded.Entity.ID != definition.ID || recorded.Context.OrgID != env.OrgID || recorded.Verb != string(audit.VerbOpsBackfillSearchProjections) {
		t.Fatalf("projection audit event = %+v", recorded)
	}
	if otherKeys := withoutProjectionMetadata(snapshotProjectionKeys(t)); !reflect.DeepEqual(withoutProjectionMetadata(beforeKeys), otherKeys) {
		t.Fatal("backfill changed a non-metadata FoundationDB key family")
	}
	updated, err := env.Stores.PropertyDefs.Get(env.Ctx, env.OrgID, definition.ID)
	if err != nil {
		t.Fatalf("read after apply: %v", err)
	}
	if updated.Search == nil || !reflect.DeepEqual(*updated.Search, projection) {
		t.Fatalf("applied projection = %+v, want %+v", updated.Search, projection)
	}
	second, err := ops.RunSearchProjectionBackfill(ctx, env.Stores.PropertyDefs, strings.NewReader(manifest), false)
	if err != nil {
		t.Fatalf("rerun backfill: %v", err)
	}
	if second.Changed != 0 || second.Unchanged != 1 || second.Missing != 0 {
		t.Fatalf("rerun result = %+v", second)
	}
	entries, err = env.Stores.OpsOutbox.ReadOutboxFrom(ctx, nil, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("rerun outbox = %d events, error = %v", len(entries), err)
	}
}

func projectionManifest(t *testing.T, orgID, propertyDefID uuid.UUID, projection node.SearchProjection) string {
	t.Helper()
	entries := []node.ProjectionManifestEntry{{OrgID: orgID, PropertyDefID: propertyDefID, Search: projection}}
	body, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal projection manifest: %v", err)
	}
	return string(body)
}
