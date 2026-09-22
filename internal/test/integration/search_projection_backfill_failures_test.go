package integration

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
)

func TestSearchProjectionBackfillRejectsInvalidManifests(t *testing.T) {
	tests := []struct {
		name string
		body func(*testing.T, uuid.UUID, node.PropertyDef, node.SearchProjection) string
		want string
	}{
		{"malformed", func(*testing.T, uuid.UUID, node.PropertyDef, node.SearchProjection) string { return `[{` }, "decode"},
		{"missing", func(*testing.T, uuid.UUID, node.PropertyDef, node.SearchProjection) string { return `[]` }, "omits"},
		{"duplicate", func(t *testing.T, org uuid.UUID, def node.PropertyDef, projection node.SearchProjection) string {
			entry := node.ProjectionManifestEntry{OrgID: org, PropertyDefID: def.ID, Search: projection}
			return projectionEntries(t, entry, entry)
		}, "duplicate"},
		{"unknown", func(t *testing.T, org uuid.UUID, _ node.PropertyDef, projection node.SearchProjection) string {
			return projectionManifest(t, org, uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff"), projection)
		}, "does not exist"},
		{"cross-organization", func(t *testing.T, _ uuid.UUID, def node.PropertyDef, projection node.SearchProjection) string {
			return projectionManifest(t, uuid.MustParse("00000000-0000-0000-0000-000000000001"), def.ID, projection)
		}, "belongs to org"},
		{"conflicting", func(t *testing.T, org uuid.UUID, def node.PropertyDef, projection node.SearchProjection) string {
			projection.Include = !projection.Include
			return projectionManifest(t, org, def.ID, projection)
		}, "different projection"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := SetupTestEnv(t)
			definitions, err := env.Stores.PropertyDefs.List(env.Ctx, env.OrgID)
			if err != nil || len(definitions) == 0 {
				t.Fatalf("list definitions: %v", err)
			}
			definition := *definitions[0]
			projection := *definition.Search
			if test.name != "conflicting" {
				definition.Search = nil
				if err := env.Stores.PropertyDefs.Set(env.Ctx, &definition); err != nil {
					t.Fatalf("clear projection: %v", err)
				}
			}
			manifest := test.body(t, env.OrgID, definition, projection)
			ctx := audit.WithOperatorPrincipal(env.Ctx, projectionTestPrincipal())
			_, err = ops.RunSearchProjectionBackfill(ctx, env.Stores.PropertyDefs, strings.NewReader(manifest), false)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("backfill error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSearchProjectionBackfillPartialFailureRerunsExactManifest(t *testing.T) {
	env := SetupTestEnv(t)
	definitions, err := env.Stores.PropertyDefs.List(env.Ctx, env.OrgID)
	if err != nil || len(definitions) < 2 {
		t.Fatalf("list two definitions: %v", err)
	}
	first, second := *definitions[0], *definitions[1]
	firstProjection, secondProjection := *first.Search, *second.Search
	first.Search = nil
	second.Search = &node.SearchProjection{Include: !secondProjection.Include, Order: secondProjection.Order, Rule: secondProjection.Rule}
	for _, definition := range []*node.PropertyDef{&first, &second} {
		if err := env.Stores.PropertyDefs.Set(env.Ctx, definition); err != nil {
			t.Fatalf("prepare definition %s: %v", definition.ID, err)
		}
	}
	manifest := projectionEntries(t,
		node.ProjectionManifestEntry{OrgID: env.OrgID, PropertyDefID: first.ID, Search: firstProjection},
		node.ProjectionManifestEntry{OrgID: env.OrgID, PropertyDefID: second.ID, Search: secondProjection},
	)
	ctx := audit.WithOperatorPrincipal(env.Ctx, projectionTestPrincipal())
	partial, err := ops.RunSearchProjectionBackfill(ctx, env.Stores.PropertyDefs, strings.NewReader(manifest), false)
	if err == nil || partial.Changed != 1 {
		t.Fatalf("partial result = %+v, error = %v", partial, err)
	}
	partialEvents, err := env.Stores.OpsOutbox.ReadOutboxFrom(ctx, nil, 10)
	if err != nil || len(partialEvents) != 1 {
		t.Fatalf("partial outbox = %d events, error = %v", len(partialEvents), err)
	}
	second.Search = nil
	if err := env.Stores.PropertyDefs.Set(env.Ctx, &second); err != nil {
		t.Fatalf("clear conflicting projection: %v", err)
	}
	rerun, err := ops.RunSearchProjectionBackfill(ctx, env.Stores.PropertyDefs, strings.NewReader(manifest), false)
	if err != nil || rerun.Changed != 1 || rerun.Unchanged != 1 {
		t.Fatalf("rerun result = %+v, error = %v", rerun, err)
	}
	completedEvents, err := env.Stores.OpsOutbox.ReadOutboxFrom(ctx, nil, 10)
	if err != nil || len(completedEvents) != 2 {
		t.Fatalf("rerun outbox = %d events, error = %v", len(completedEvents), err)
	}
}

func TestSearchProjectionBackfillConcurrentDeclarations(t *testing.T) {
	env := SetupTestEnv(t)
	definitions, err := env.Stores.PropertyDefs.List(env.Ctx, env.OrgID)
	if err != nil || len(definitions) == 0 {
		t.Fatalf("list definitions: %v", err)
	}
	definition := *definitions[0]
	projection := *definition.Search
	definition.Search = nil
	if err := env.Stores.PropertyDefs.Set(env.Ctx, &definition); err != nil {
		t.Fatalf("clear projection: %v", err)
	}
	other := projection
	other.Include = !other.Include
	manifests := []string{projectionManifest(t, env.OrgID, definition.ID, projection), projectionManifest(t, env.OrgID, definition.ID, other)}
	results := make([]error, len(manifests))
	ctx := audit.WithOperatorPrincipal(env.Ctx, projectionTestPrincipal())
	var wait sync.WaitGroup
	for index, manifest := range manifests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, results[index] = ops.RunSearchProjectionBackfill(ctx, env.Stores.PropertyDefs, strings.NewReader(manifest), false)
		}()
	}
	wait.Wait()
	if (results[0] == nil) == (results[1] == nil) {
		t.Fatalf("Concurrent backfill results were %v. Exactly one backfill call must succeed.", results)
	}
}

func projectionTestPrincipal() audit.OperatorPrincipal {
	return audit.OperatorPrincipal{ID: uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca30"), Email: "operator@example.invalid", Name: "Operator", Source: "test"}
}

func projectionEntries(t *testing.T, entries ...node.ProjectionManifestEntry) string {
	t.Helper()
	sort.Slice(entries, func(i, j int) bool { return entries[i].PropertyDefID.String() < entries[j].PropertyDefID.String() })
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("encode projection entries: %v", err)
	}
	return string(data)
}
