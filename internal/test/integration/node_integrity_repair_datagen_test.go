package integration_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/response"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// datagenRepairResult decodes the fields of the ops qa datagen repair result
// that the test reads.
type datagenRepairResult struct {
	OrgID    uuid.UUID                 `json:"org_id"`
	Preview  *ops.NodeIntegrityPreview `json:"preview"`
	Applied  *ops.NodeIntegrityApply   `json:"applied"`
	Rerun    *ops.NodeIntegrityApply   `json:"rerun"`
	Verified bool                      `json:"verified"`
}

// TestNodeIntegrityRepairDatagenProvesEveryClass runs
// ops qa datagen repair --commit through the command boundary. It requires
// the command to refuse without a write marker before creating an
// organization. With the local marker, it requires a verified run in an
// organization other than the test organization, with every entry applied
// and every entry unchanged after the second execution. It requires the
// preview to include every repair class and each remaining node to belong
// to the datagen organization with exactly one child_of edge to its
// parent_id. It requires the repair to delete one node and the repair
// directory to contain one export file.
func TestNodeIntegrityRepairDatagenProvesEveryClass(t *testing.T) {
	env := integration.SetupTestEnv(t)
	refused, err := runDatagenRepairCommand(t, "")
	if err == nil || !strings.Contains(err.Error(), "TACK_DATAGEN_ALLOW_TARGET") || refused.OrgID != uuid.Nil || refused.Applied != nil {
		t.Fatalf("datagen repair without a write marker = %+v, %v, want a refusal before any write", refused, err)
	}

	result, err := runDatagenRepairCommand(t, "local")
	if err != nil || !result.Verified || result.Preview == nil || result.Applied == nil || result.Rerun == nil {
		t.Fatalf("datagen repair = %+v, %v, want a verified run", result, err)
	}
	entries := len(result.Applied.Entries)
	if result.Applied.Counts[ops.RepairApplyApplied] != entries || result.Rerun.Counts[ops.RepairApplyUnchanged] != entries {
		t.Fatalf("apply counts %v and rerun counts %v, want %d applied and then unchanged entries", result.Applied.Counts, result.Rerun.Counts, entries)
	}
	if result.OrgID == env.OrgID {
		t.Fatalf("datagen repair wrote organization %s, want a separate datagen organization", result.OrgID)
	}
	classes := map[string]bool{}
	for _, entry := range result.Preview.Entries {
		for _, class := range entry.Classes {
			classes[class] = true
		}
	}
	for _, class := range []string{"multiple_child_of_edges", "absent_parent", "legacy_status", "printed_parent", "absent_parent_uuid", "test_fixture"} {
		if !classes[class] {
			t.Fatalf("datagen repair classes = %v, want %s", classes, class)
		}
	}
	deleted := 0
	for _, entry := range result.Applied.Entries {
		view, err := env.Stores.Views.Get(env.Ctx, entry.NodeID)
		if err != nil {
			t.Fatalf("read node %s: %v", entry.NodeID, err)
		}
		if view == nil {
			deleted++
			continue
		}
		edges, err := env.Stores.Relationships.ListBySource(env.Ctx, result.OrgID, entry.NodeID, node.RelChildOf)
		parentID := propText(t, view.Props, "parent_id")
		if err != nil || view.OrgID != result.OrgID || len(edges) != 1 || edges[0].TargetID.String() != parentID {
			t.Fatalf("node %s in organization %s has parent_id %s and child_of edges %+v, %v, want one edge to its parent in %s",
				entry.NodeID, view.OrgID, parentID, edges, err, result.OrgID)
		}
	}
	exports, err := filepath.Glob(filepath.Join(repairDirectory(t), "*.json"))
	if deleted != 1 || err != nil || len(exports) != 1 {
		t.Fatalf("deleted %d nodes and found exports %v, %v, want one deleted comment and its export", deleted, exports, err)
	}
}

// runDatagenRepairCommand runs ops qa datagen repair --commit with --execute
// against the test FoundationDB and ledger. It uses the test repair
// directory and target as TACK_DATAGEN_ALLOW_TARGET, then returns the
// decoded result and the command error.
func runDatagenRepairCommand(t *testing.T, target string) (datagenRepairResult, error) {
	t.Helper()
	cfg := &config.Config{
		DatabaseURL: testenv.Ledger(t), FDBClusterFile: testenv.FoundationDB(t), FDBTransactionTimeout: repairTransactionTimeout,
		RepairDir: repairDirectory(t), DatagenAllowTarget: target,
	}
	factory := cli.System(cfg)
	var output bytes.Buffer
	factory.Out, factory.Err = &output, &bytes.Buffer{}
	pool, err := pgxpool.New(t.Context(), testenv.Ledger(t))
	if err != nil {
		t.Fatalf("open audit ledger pool: %v", err)
	}
	defer pool.Close()
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	root := repairCommandRoot(factory)
	root.SetArgs([]string{
		"--execute", "--output", "json", "--operator-id", repairOperatorID,
		"--operator-email", "repair@example.com", "--operator-name", "Repair Test",
		"ops", "qa", "datagen", "repair", "--commit",
	})
	runErr := root.Execute()
	var result datagenRepairResult
	start := bytes.IndexByte(output.Bytes(), '{')
	if start < 0 {
		return result, runErr
	}
	var document response.JSONDocument
	if err := json.Unmarshal(output.Bytes()[start:], &document); err != nil {
		t.Fatalf("decode datagen repair output %q: %v", output.String(), err)
	}
	if err := json.Unmarshal(document.Result, &result); err != nil {
		t.Fatalf("decode datagen repair result %s: %v", document.Result, err)
	}
	return result, runErr
}
