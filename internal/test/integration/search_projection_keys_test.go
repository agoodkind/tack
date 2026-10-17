package integration

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/testenv"
)

// TestSearchProjectionRepairClearsWarning verifies that search finds a node
// by its valid name despite an undeclared property. ops search verify must
// report the property warning instead of an exclusion. Declaring the property
// must index its value and clear the warning.
func TestSearchProjectionRepairClearsWarning(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	entry := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	drainSearchWork(t, fixture.Worker, 2000)

	name := opaqueSearchKey("p")
	affected := putUndeclaredNode(t, fixture, kind, entry, name, "quartz lantern harbor")
	drainSearchWork(t, fixture.Worker, 2000)
	if results := callEverySearchPage(t, "undeclared node", fixture.Harness); !slices.Contains(results.IDs, affected) {
		t.Fatalf("search for the node name returned %v, want node %s", results.IDs, affected)
	}
	report := runSearchVerifyWarnings(t, fixture.Config)
	if slices.ContainsFunc(report.Exclusions, func(exclusion verifyNode) bool { return exclusion.NodeID == affected.String() }) {
		t.Fatalf("ops search verify excludes node %s for an undeclared property", affected)
	}
	warning, warned := report.warning(affected, name)
	if !warned || warning.Reason != string(node.DiagnosticUndeclared) || warning.PropertyDefID != "" || warning.Revision == "" {
		t.Fatalf("ops search verify warning of node %s = %+v, listed %t, want an undeclared property warning", affected, warning, warned)
	}

	definition := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: workspace.OrgID, Name: name, Type: node.PropertyType(opaqueSearchKey("t")),
		Search: &node.SearchProjection{Include: true, Order: 2, Rule: node.TextRule{Mode: node.TextRuleScalar}},
	}
	if err := fixture.Stores.PropertyDefs.Set(t.Context(), definition); err != nil {
		t.Fatalf("declare property %s: %v", name, err)
	}
	drainSearchWork(t, fixture.Worker, 2000)
	if results := callEverySearchPage(t, "quartz lantern harbor", fixture.Harness); !slices.Contains(results.IDs, affected) {
		t.Fatalf("search for the repaired value returned %v, want node %s", results.IDs, affected)
	}
	if _, warned := runSearchVerifyWarnings(t, fixture.Config).warning(affected, name); warned {
		t.Fatalf("ops search verify still lists the warning of node %s after the repair", affected)
	}
}

// verifyNode decodes the node ID of one exclusion from the JSON output of
// ops search verify into its single NodeID field.
type verifyNode struct {
	NodeID string `json:"node_id"`
}

// verifyWarning decodes one property warning from the JSON output of
// ops search verify, including the node ID, property name, property definition
// ID, revision, and reason.
type verifyWarning struct {
	NodeID        string `json:"node_id"`
	PropertyName  string `json:"property_name"`
	PropertyDefID string `json:"property_def_id"`
	Revision      string `json:"revision"`
	Reason        string `json:"reason"`
}

// verifyWarningReport decodes exclusions and property warnings from the JSON
// output of ops search verify.
type verifyWarningReport struct {
	Exclusions []verifyNode    `json:"exclusions"`
	Warnings   []verifyWarning `json:"warnings"`
}

func (report verifyWarningReport) warning(nodeID uuid.UUID, propertyName string) (verifyWarning, bool) {
	for _, warning := range report.Warnings {
		if warning.NodeID == nodeID.String() && warning.PropertyName == propertyName {
			return warning, true
		}
	}
	return verifyWarning{}, false
}

// runSearchVerifyWarnings runs the audited ops search verify command through
// the rendered Cobra tree using the test ledger's audit outbox. It decodes the
// JSON output into a verifyWarningReport and fails the test if decoding
// fails. It ignores the command error.
func runSearchVerifyWarnings(t *testing.T, cfg *config.Config) verifyWarningReport {
	t.Helper()
	factory := cli.System(cfg)
	var output bytes.Buffer
	factory.Out = &output
	pool, err := pgxpool.New(t.Context(), testenv.Ledger(t))
	if err != nil {
		t.Fatalf("open audit ledger pool: %v", err)
	}
	defer pool.Close()
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	root := searchCommandRoot(factory)
	root.SetArgs([]string{
		"--execute", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
		"--operator-email", "operator@example.com", "--operator-name", "Search Test",
		"ops", "search", "verify",
	})
	runErr := root.Execute()
	var report verifyWarningReport
	if err := json.NewDecoder(&output).Decode(&report); err != nil {
		t.Fatalf("decode ops search verify output (command error %v): %v", runErr, err)
	}
	return report
}

type projectionKeyValue struct {
	family string
	value  string
}

func snapshotProjectionKeys(t *testing.T) map[string]projectionKeyValue {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), testTransactionTimeout)
	if err != nil {
		t.Fatalf("open projection snapshot database: %v", err)
	}
	prefix := fdbadapter.TestPrefixRange()
	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		t.Fatalf("create projection snapshot range: %v", err)
	}
	transaction, err := database.CreateTransaction()
	if err != nil {
		t.Fatalf("create projection snapshot transaction: %v", err)
	}
	defer transaction.Cancel()
	entries, err := transaction.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		t.Fatalf("read projection snapshot: %v", err)
	}
	snapshot := make(map[string]projectionKeyValue, len(entries))
	for _, entry := range entries {
		parts, decodeErr := tuple.Unpack(entry.Key[len(prefix):])
		if decodeErr != nil || len(parts) == 0 {
			t.Fatalf("decode projection snapshot key: %v", decodeErr)
		}
		family, ok := parts[0].(string)
		if !ok {
			t.Fatalf("projection snapshot key family is not a string: %v", parts[0])
		}
		snapshot[string(entry.Key)] = projectionKeyValue{family: family, value: string(entry.Value)}
	}
	return snapshot
}

// withoutProjectionMetadata returns a copy of snapshot without the property
// definition records, their property-name index entries, and the audit
// outbox entries. A backfill must leave every remaining key unchanged,
// including the search work, projection digest, and scan keys.
func withoutProjectionMetadata(snapshot map[string]projectionKeyValue) map[string]projectionKeyValue {
	other := make(map[string]projectionKeyValue)
	for key, value := range snapshot {
		if value.family == "property_def" || value.family == "property_def_by_name" || value.family == "ops_outbox" {
			continue
		}
		other[key] = value
	}
	return other
}
