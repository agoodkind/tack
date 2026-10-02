package integration

import (
	"testing"

	"goodkind.io/tack/internal/datagen"
)

const (
	// searchGuardSeed is the fixed seed of the production guard test. Its
	// planned organization must never exist.
	searchGuardSeed = 538_000_001
	// searchDatagenSeed is the fixed seed of TestSearchDatagen. The
	// verification also bootstraps the foreign organization at
	// searchDatagenSeed+1. Harness seeds start at the current Unix time in
	// nanoseconds, far above both values.
	searchDatagenSeed = 538_000_002
)

// TestSearchDatagen runs the QA search verification against a local target
// with public search enabled. The verification creates an isolated opaque
// organization, calls tack_search through authenticated MCP requests, and
// checks relevance, final-page text, excluded text, edits, deletion, and
// complete continuation. It then bootstraps a second organization with
// matching text and checks result isolation between the organizations,
// refusal under the other organization's entry node, cursor replay, and
// refusal of a removed member's open cursor and new search. The second run
// reuses the seed and requires the restored membership of the removed
// member.
func TestSearchDatagen(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	cfg := *fixture.Config
	cfg.DatagenAllowTarget = "local"
	for run := 1; run <= 2; run++ {
		if err := datagen.VerifySearchWithSeed(t.Context(), &cfg, searchDatagenSeed); err != nil {
			t.Fatalf("verify search run %d with seed %d: %v", run, searchDatagenSeed, err)
		}
	}
}

// TestSearchDatagenProductionGuard requires the verification to refuse a
// target without the QA marker and a target that identifies production,
// before it writes any metadata or node.
func TestSearchDatagenProductionGuard(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	unmarked := *fixture.Config
	unmarked.DatagenAllowTarget = ""
	production := *fixture.Config
	production.DatagenAllowTarget = "qa"
	production.AuditWriterDSN = "postgres://writer@tack.home.goodkind.io:5433/yugabyte"
	scale, err := datagen.ParseScale("small")
	if err != nil {
		t.Fatalf("parse scale: %v", err)
	}
	if err := datagen.VerifySearchWithSeed(t.Context(), &unmarked, searchGuardSeed); err == nil {
		t.Fatal("a target without the QA marker was accepted")
	}
	if err := datagen.VerifySearchWithSeed(t.Context(), &production, searchGuardSeed); err == nil {
		t.Fatal("a target that identifies production was accepted")
	}
	planned := datagen.PlanIdentities(&unmarked, searchGuardSeed, scale)
	for _, workspace := range planned.Workspaces {
		view, err := fixture.Stores.Views.Get(t.Context(), workspace.OrgID)
		if err != nil || view != nil {
			t.Fatalf("refused verification left organization %s: view %v, error %v", workspace.OrgID, view, err)
		}
	}
}
