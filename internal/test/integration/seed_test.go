package integration

import (
	"sort"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// TestSeedIsIdempotent runs the seed twice in the same test prefix. Both
// runs must produce identical NodeType IDs and identical PropertyDef IDs.
//
// This is the test that would have caught yesterday's incident. The rewrite
// shipped without an "identifier" PropertyDef, so the seed produced a
// different set of PropertyDefs than the resolver expected. Re-seeding never
// surfaced the gap because nothing tested for it.
func TestSeedIsIdempotent(t *testing.T) {
	env := SetupTestEnv(t)

	// Capture the first run's IDs.
	firstTypes := listNodeTypes(t, env)
	firstProps := listPropertyDefs(t, env)

	if len(firstTypes) == 0 {
		t.Fatal("expected NodeTypes after first seed, got zero")
	}
	if len(firstProps) == 0 {
		t.Fatal("expected PropertyDefs after first seed, got zero")
	}

	// Re-run the seed against the same orgID. Same prefix, same orgID,
	// same inputs everywhere.
	env.Seeder.SeedOrg(env.Ctx, env.OrgID)

	secondTypes := listNodeTypes(t, env)
	secondProps := listPropertyDefs(t, env)

	if len(secondTypes) != len(firstTypes) {
		t.Fatalf("NodeType count drift: first=%d second=%d", len(firstTypes), len(secondTypes))
	}
	if len(secondProps) != len(firstProps) {
		t.Fatalf("PropertyDef count drift: first=%d second=%d", len(firstProps), len(secondProps))
	}

	for slug, firstID := range firstTypes {
		if secondID := secondTypes[slug]; secondID != firstID {
			t.Errorf("NodeType %q ID drift: first=%s second=%s", slug, firstID, secondID)
		}
	}
	for name, firstID := range firstProps {
		if secondID := secondProps[name]; secondID != firstID {
			t.Errorf("PropertyDef %q ID drift: first=%s second=%s", name, firstID, secondID)
		}
	}
}

// TestSeedHasIdentifierPropertyDef pins the resolver/seed contract that
// yesterday's bug broke. The resolver looks up scope nodes via
// Props["identifier"]. The seed must register a PropertyDef named
// "identifier" with Indexed=true so the secondary index gets written when
// projects are created.
//
// Removing this PropertyDef will fail this test, which is the proof the
// suite would have caught yesterday.
func TestSeedHasIdentifierPropertyDef(t *testing.T) {
	env := SetupTestEnv(t)

	defs := listPropertyDefRecords(t, env)
	for _, d := range defs {
		if d.Name == "identifier" {
			if !d.Indexed {
				t.Fatalf(`PropertyDef "identifier" exists but Indexed=false; resolver lookups will silently miss`)
			}
			return
		}
	}
	t.Fatal(`PropertyDef "identifier" missing; resolver cannot find scope nodes by identifier`)
}

func TestSeedHasStateIDRequiredTodoDefaultReference(t *testing.T) {
	env := SetupTestEnv(t)

	defs := listPropertyDefRecords(t, env)
	for _, d := range defs {
		if d.Name != "state_id" {
			continue
		}
		if !d.Required {
			t.Fatalf(`PropertyDef "state_id" exists but Required=false`)
		}
		if d.DefaultReference == nil {
			t.Fatalf(`PropertyDef "state_id" missing default reference`)
		}
		if d.DefaultReference.Scope != node.DefaultReferenceScopeCreateScope {
			t.Fatalf("state_id default scope = %q, want %q", d.DefaultReference.Scope, node.DefaultReferenceScopeCreateScope)
		}
		if d.DefaultReference.Reference != "Todo" {
			t.Fatalf("state_id default reference = %q, want %q", d.DefaultReference.Reference, "Todo")
		}
		return
	}
	t.Fatal(`PropertyDef "state_id" missing`)
}

// listNodeTypes returns all NodeTypes for the test org, keyed by slug.
func listNodeTypes(t *testing.T, env *TestEnv) map[string]uuid.UUID {
	t.Helper()
	types, err := env.Stores.NodeTypes.List(env.Ctx, env.OrgID)
	if err != nil {
		t.Fatalf("list node types: %v", err)
	}
	out := make(map[string]uuid.UUID, len(types))
	for _, nt := range types {
		out[nt.Slug] = nt.ID
	}
	return out
}

// listPropertyDefs returns all PropertyDefs for the test org, keyed by name.
func listPropertyDefs(t *testing.T, env *TestEnv) map[string]uuid.UUID {
	t.Helper()
	defs, err := env.Stores.PropertyDefs.List(env.Ctx, env.OrgID)
	if err != nil {
		t.Fatalf("list property defs: %v", err)
	}
	out := make(map[string]uuid.UUID, len(defs))
	for _, d := range defs {
		out[d.Name] = d.ID
	}
	return out
}

// listPropertyDefRecords returns all PropertyDef records sorted by name so
// tests get deterministic ordering when iterating.
func listPropertyDefRecords(t *testing.T, env *TestEnv) []*node.PropertyDef {
	t.Helper()
	defs, err := env.Stores.PropertyDefs.List(env.Ctx, env.OrgID)
	if err != nil {
		t.Fatalf("list property defs: %v", err)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}
