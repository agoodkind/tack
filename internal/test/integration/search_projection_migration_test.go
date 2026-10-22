package integration_test

import (
	"errors"
	"strings"
	"testing"

	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	migrationPageBytes = 64
	migrationNodeCount = 6
)

// migrationText is longer than three pages at migrationPageBytes.
var migrationText = strings.Repeat("amber lattice orchard ", 12)

// finishMetadataPass completes the rescan's metadata pass and leaves its
// node pass pending.
func (env *migrationEnv) finishMetadataPass(t *testing.T) {
	t.Helper()
	env.driveRescan(t, func(work searchdomain.Work) bool { return work.Phase == searchdomain.PhasePages }, func(int) {})
}

// TestSearchProjectionMigrationReindexesUnchangedDefinitions checks that
// activation reindexes every node in a fixture that simulates an older
// release. Every definition, node type, and node value stays unchanged.
// The organization does not have pending search work before activation.
// After activation and before the node pass, a fresh read must return the
// unchanged revision under a new projection. A cursor for that revision
// under the earlier projection must fail with ErrContentChanged. The
// worker must retire the older revisions' pages in the same serving index.
// A repeated migration must reindex every node under another projection.
func TestSearchProjectionMigrationReindexesUnchangedDefinitions(t *testing.T) {
	env := newMigrationEnv(t, migrationPageBytes)
	nodes := env.putNodes(t, migrationNodeCount, migrationText)
	env.runIdle(t)
	initial := env.revisions(t, nodes)
	initialProjection := env.pages(t, nodes[0], false)[0].ProjectionVersion

	reader := env.Stores.SearchContent(env.Stores.SearchPolicySet())
	request := searchdomain.ContentRequest{NodeID: nodes[0], Cursor: "", ProjectionConfig: "", AccessVersions: nil, MaxBytes: migrationPageBytes, SearchGeneration: 0}
	opening, err := reader.Content(integration.WithTestOperation(t, t.Context()), request)
	if err != nil || opening.Done {
		t.Fatalf("read the first page = done %t, err %v; want a continuing page", opening.Done, err)
	}

	env.forgetProjectionState(t)
	env.finishMetadataPass(t)
	fresh, err := reader.Content(integration.WithTestOperation(t, t.Context()), request)
	if err != nil || fresh.Revision != opening.Revision || fresh.ProjectionVersion == opening.ProjectionVersion {
		t.Fatalf("fresh read after activation = revision %s projection %s err %v, want revision %s under a new projection",
			fresh.Revision, fresh.ProjectionVersion, err, opening.Revision)
	}
	request.Cursor = opening.NextCursor
	if _, err := reader.Content(integration.WithTestOperation(t, t.Context()), request); !errors.Is(err, node.ErrContentChanged) {
		t.Fatalf("continue an older projection cursor = %v, want ErrContentChanged", err)
	}
	env.runIdle(t)
	migrated := env.requireMigrated(t, nodes, initial, initialProjection)
	for _, nodeID := range nodes {
		env.requireRetiredRevision(t, nodeID, initial[nodeID])
	}

	again := env.revisions(t, nodes)
	env.forgetProjectionState(t)
	env.runIdle(t)
	env.requireMigrated(t, nodes, again, migrated)
}
