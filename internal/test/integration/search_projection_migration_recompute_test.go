package integration_test

import (
	"strings"
	"testing"
	"time"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/testenv"
)

// migrationDefinitionCount requires two metadata pages of 100 definitions.
const migrationDefinitionCount = 150

// putUnusedDefinitions stores migrationDefinitionCount included definitions
// that nodes do not use.
func (env *migrationEnv) putUnusedDefinitions(t *testing.T) {
	t.Helper()
	for range migrationDefinitionCount {
		env.putDefinition(t, uniqueKey("p"), true)
	}
}

// includeExcludedDefinition changes the excluded definition to include its
// values in search text.
func (env *migrationEnv) includeExcludedDefinition(t *testing.T) {
	t.Helper()
	definitions, err := env.Stores.PropertyDefs.List(t.Context(), env.OrgID)
	if err != nil {
		t.Fatalf("list property definitions: %v", err)
	}
	for _, definition := range definitions {
		if definition.Name == env.ExcludedKey {
			definition.Search = &node.SearchProjection{Include: true, Order: 1, Rule: node.TextRule{Mode: node.TextRuleScalar}}
			if err := env.Stores.PropertyDefs.Set(t.Context(), definition); err != nil {
				t.Fatalf("include property definition %s: %v", env.ExcludedKey, err)
			}
			return
		}
	}
	t.Fatalf("organization %s has no definition %s", env.OrgID, env.ExcludedKey)
}

// TestSearchProjectionMigrationResumesInterruptedRecomputation checks that
// recomputation resumes at its checkpoint after one metadata page and an
// expired claim. It creates new stores and a worker to simulate a process
// restart. The migration must then reindex every node under one new
// projection.
func TestSearchProjectionMigrationResumesInterruptedRecomputation(t *testing.T) {
	env := newMigrationEnv(t, migrationPageBytes)
	nodes := env.putNodes(t, migrationNodeCount, migrationText)
	env.putUnusedDefinitions(t)
	env.runIdle(t)
	initial := env.revisions(t, nodes)
	initialProjection := env.pages(t, nodes[0], false)[0].ProjectionVersion

	env.forgetProjectionState(t)
	store := env.Stores.SearchWork(clock.Wall{})
	opening, err := store.Claim(t.Context(), searchdomain.WorkClassRescan, "interrupted-process", time.Minute)
	if err != nil || opening.Phase != searchdomain.PhaseMetadata {
		t.Fatalf("claim the recomputation = %+v err %v, want metadata work", opening, err)
	}
	if err := env.Worker.Process(t.Context(), opening); err != nil {
		t.Fatalf("process the first metadata page: %v", err)
	}
	abandoned, err := store.Claim(t.Context(), searchdomain.WorkClassRescan, "interrupted-process", 2*time.Second)
	if err != nil || abandoned.Phase != searchdomain.PhaseMetadata || abandoned.Cursor == "" {
		t.Fatalf("claim after one metadata page = %+v err %v, want a metadata checkpoint", abandoned, err)
	}
	time.Sleep(time.Until(abandoned.LeaseUntil))

	restartedStores, err := fdbadapter.NewStores(testenv.FoundationDB(t), 5*time.Second, nil)
	if err != nil {
		t.Fatalf("open the restarted process stores: %v", err)
	}
	restartedStores.EnableSearchWork()
	restarted := restartedStores.SearchWork(clock.Wall{})
	resumed, err := restarted.Claim(t.Context(), searchdomain.WorkClassRescan, "restarted-process", time.Minute)
	if err != nil || resumed.Phase != searchdomain.PhaseMetadata || resumed.Cursor != abandoned.Cursor {
		t.Fatalf("restarted claim = %+v err %v, want the metadata checkpoint %q", resumed, err, abandoned.Cursor)
	}
	if err := restarted.Yield(t.Context(), resumed); err != nil {
		t.Fatalf("yield resumed recomputation: %v", err)
	}
	env.Worker = env.newWorker(t, restartedStores)
	env.runIdle(t)
	env.requireMigrated(t, nodes, initial, initialProjection)
}

// TestSearchProjectionMigrationRestartsAfterConcurrentMetadataWrite checks
// that a definition write during recomputation restarts the metadata pass
// before activation. The restarted pass must read every metadata page
// again. Every node must then include the values of the changed definition
// in its search text under one new projection.
func TestSearchProjectionMigrationRestartsAfterConcurrentMetadataWrite(t *testing.T) {
	env := newMigrationEnv(t, migrationPageBytes)
	nodes := env.putNodes(t, migrationNodeCount, migrationText)
	env.putUnusedDefinitions(t)
	env.runIdle(t)
	inPages := func(work searchdomain.Work) bool { return work.Phase == searchdomain.PhasePages }

	env.forgetProjectionState(t)
	_, quiet := env.driveRescan(t, inPages, func(int) {})
	env.runIdle(t)
	initial := env.revisions(t, nodes)
	initialProjection := env.pages(t, nodes[0], false)[0].ProjectionVersion

	env.forgetProjectionState(t)
	_, concurrent := env.driveRescan(t, inPages, func(processed int) {
		if processed == 1 {
			env.includeExcludedDefinition(t)
		}
	})
	if quiet < 2 || concurrent != 2*quiet {
		t.Fatalf("metadata slices = %d with a concurrent write and %d without, want twice as many", concurrent, quiet)
	}
	env.runIdle(t)
	env.requireMigrated(t, nodes, initial, initialProjection)
	for _, nodeID := range nodes {
		var text strings.Builder
		for _, page := range env.readerPages(t, nodeID) {
			text.WriteString(page.Text[page.OverlapBytes:])
		}
		if !strings.Contains(text.String(), migrationExcluded) {
			t.Fatalf("node %s text omits the concurrently included value", nodeID)
		}
	}
}
