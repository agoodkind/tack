//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/response"
	"goodkind.io/tack/internal/test/integration"
)

// addRepairBackfillCases adds search projection and node integrity backfill
// cases that execute manifests without changing nodes.
func addRepairBackfillCases(cases map[string]coverageCase) {
	cases["command ops backfill once-search-projections"] = commandCase(audit.VerbOpsBackfillSearchProjections, audit.OutcomeOK,
		argsCommand(func(t *testing.T, _ *coverageEnv) []string {
			return []string{"ops", "backfill", "once-search-projections", "--manifest", writeCoverageFile(t, "projections.json", []byte("[]"))}
		}))
	cases["command ops backfill once-node-integrity"] = commandCase(audit.VerbOpsBackfillNodeIntegrity, audit.OutcomeOK,
		argsCommand(func(t *testing.T, env *coverageEnv) []string {
			return []string{"ops", "backfill", "once-node-integrity", "--manifest", env.integrityManifest(t)}
		}))
}

// writeCoverageFile writes the supplied contents to a file in a temporary
// directory and returns its path.
func writeCoverageFile(t *testing.T, name string, contents []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("Writing the temporary file failed; the command case requires file %s: %v", name, err)
	}
	return path
}

// integrityManifest creates an issue with two child_of relationships and
// freezes a repair request in a dry run. It writes the returned manifest to a
// temporary file for the case to execute with --execute. Execution does not
// change nodes because the manifest entry does not request deletion.
func (env *coverageEnv) integrityManifest(t *testing.T) string {
	t.Helper()
	fixture := env.fixture(t)
	issue, epic := fixture.create(t, "issue"), fixture.create(t, "epic")
	if err := env.stores(t).Relationships.Add(integration.WithTestOperation(t, t.Context()), &node.Relationship{
		OrgID: env.server.orgID, SourceID: issue, RelationType: node.RelChildOf, TargetID: epic,
		CreatedBy: env.server.userID, CreatedAt: time.Now().UTC(), Props: nil,
	}); err != nil {
		t.Fatalf("Adding the issue's child_of relationship to the epic failed; the repair case requires that relationship: %v", err)
	}
	request, err := json.Marshal(ops.NodeIntegrityRequest{Version: 1, Ticket: "TACK-582", Entries: []ops.NodeIntegrityEntry{{
		RepairID: "coverage-" + uuid.NewString()[:8], NodeID: issue, NodeType: "issue", Status: ops.RepairEntryExecutable,
		Classes: []string{"multiple_child_of_edges"},
		Evidence: []ops.RepairEvidence{{
			Kind: ops.RepairEvidenceInferred, Reference: "TACK-583 coverage", Summary: "The epic is the most specific linked container.",
		}},
		Result: ops.RepairResult{
			Props:       map[string]json.RawMessage{"parent_id": json.RawMessage(strconv.Quote(epic.String()))},
			RemoveEdges: []ops.RepairEdge{{RelationType: node.RelChildOf, TargetID: fixture.nodes["project"]}},
		},
	}}})
	if err != nil {
		t.Fatalf("Repair request encoding failed; the dry run requires a JSON request: %v", err)
	}
	output, err := env.dryRun(t, "ops", "backfill", "once-node-integrity", "--request", writeCoverageFile(t, "request.json", request))
	if err != nil {
		t.Fatalf("The node integrity dry run failed; the test expects it to freeze the repair request: %v\n%s", err, output)
	}
	start := bytes.IndexByte([]byte(output), '{')
	var document response.JSONDocument
	if start < 0 || json.Unmarshal([]byte(output[start:]), &document) != nil {
		t.Fatalf("The dry run did not return a readable JSON document; the test requires one to extract the manifest:\n%s", output)
	}
	var result struct {
		Manifest json.RawMessage `json:"manifest"`
	}
	if err := json.Unmarshal(document.Result, &result); err != nil || len(result.Manifest) == 0 {
		t.Fatalf("The repair result does not contain a readable, nonempty manifest; the test requires one in result %s: %v", document.Result, err)
	}
	return writeCoverageFile(t, "manifest.json", result.Manifest)
}
