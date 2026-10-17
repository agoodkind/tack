package integration_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/response"
	"goodkind.io/tack/internal/testenv"
)

// repairOperatorID identifies the operator for each repair command in these tests.
const repairOperatorID = "019dd226-440e-729a-a442-281aaf73ca31"

// repairTransactionTimeout bounds each FoundationDB transaction of the
// repair command and of the key reads in these tests.
const repairTransactionTimeout = 5 * time.Second

// repairCommandResult contains a repair command's decoded JSON result.
type repairCommandResult struct {
	Command  string                     `json:"command"`
	DryRun   bool                       `json:"dry_run"`
	Manifest *ops.NodeIntegrityManifest `json:"manifest"`
	Preview  *ops.NodeIntegrityPreview  `json:"preview"`
	Applied  *ops.NodeIntegrityApply    `json:"applied"`
}

// runRepairCommand executes ops backfill once-node-integrity with args
// using the rendered command tree and decodes the JSON result.
func runRepairCommand(t *testing.T, args ...string) (repairCommandResult, error) {
	t.Helper()
	cfg := &config.Config{
		DatabaseURL: testenv.Ledger(t), FDBClusterFile: testenv.FoundationDB(t), FDBTransactionTimeout: repairTransactionTimeout,
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
	root.SetArgs(append([]string{
		"--output", "json", "--operator-id", repairOperatorID,
		"--operator-email", "repair@example.com", "--operator-name", "Repair Test",
		"ops", "backfill", "once-node-integrity",
	}, args...))
	runErr := root.Execute()
	var result repairCommandResult
	start := bytes.IndexByte(output.Bytes(), '{')
	if start < 0 {
		return result, runErr
	}
	var document response.JSONDocument
	if err := json.Unmarshal(output.Bytes()[start:], &document); err != nil {
		t.Fatalf("decode repair command output %q: %v", output.String(), err)
	}
	if err := json.Unmarshal(document.Result, &result); err != nil {
		t.Fatalf("decode repair command result %s: %v", document.Result, err)
	}
	return result, runErr
}

// repairCommandRoot registers the ops commands in a new clispec registry
// and renders them as Cobra commands under a root command named tack.
// It registers the global flags on the root and sets factory's operator
// identity source.
func repairCommandRoot(factory *cli.Factory) *cobra.Command {
	registry := clispec.NewRegistry()
	ops.RegisterCommands(registry, factory)
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	factory.RegisterGlobalFlags(root)
	for _, command := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(command)
	}
	factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
	return root
}

// writeRepairDocument writes value as JSON in a temporary directory and
// returns the file path.
func writeRepairDocument[Document ops.NodeIntegrityRequest | ops.NodeIntegrityManifest](t *testing.T, name string, value Document) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// freezeRepairRequest runs the command's request dry run and returns its
// frozen manifest.
func freezeRepairRequest(t *testing.T, request ops.NodeIntegrityRequest) ops.NodeIntegrityManifest {
	t.Helper()
	result, err := runRepairCommand(t, "--request", writeRepairDocument(t, "request.json", request))
	if err != nil {
		t.Fatalf("freeze repair request: %v", err)
	}
	if result.Manifest == nil || !result.DryRun {
		t.Fatalf("freeze result = %+v, want a dry run with a manifest", result)
	}
	return *result.Manifest
}

// previewRepairManifest runs the command's manifest dry run and returns its preview.
func previewRepairManifest(t *testing.T, manifest ops.NodeIntegrityManifest) ops.NodeIntegrityPreview {
	t.Helper()
	result, err := runRepairCommand(t, "--manifest", writeRepairDocument(t, "manifest.json", manifest))
	if err != nil {
		t.Fatalf("preview repair manifest: %v", err)
	}
	if result.Preview == nil || !result.DryRun {
		t.Fatalf("preview result = %+v, want a dry run with a preview", result)
	}
	return *result.Preview
}

// previewEntry finds the entry matching repairID or fails the test.
func previewEntry(t *testing.T, preview ops.NodeIntegrityPreview, repairID string) ops.NodeIntegrityEntryPreview {
	t.Helper()
	for _, entry := range preview.Entries {
		if entry.RepairID == repairID {
			return entry
		}
	}
	t.Fatalf("preview has no entry %s: %+v", repairID, preview.Entries)
	return ops.NodeIntegrityEntryPreview{}
}

// storedKeyValues returns all FoundationDB keys and values under the active test prefix.
func storedKeyValues(t *testing.T) map[string]string {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), repairTransactionTimeout)
	if err != nil {
		t.Fatalf("open database to read keys: %v", err)
	}
	keyRange, err := fdb.PrefixRange(fdbadapter.TestPrefixRange())
	if err != nil {
		t.Fatalf("create test prefix range: %v", err)
	}
	items, err := database.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		t.Fatalf("read test prefix range: %v", err)
	}
	stored := map[string]string{}
	for _, item := range items.([]fdb.KeyValue) {
		stored[string(item.Key)] = string(item.Value)
	}
	return stored
}
