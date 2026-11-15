//go:build runtimeaudit

package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// provisionCoverageIndex is the physical index that ops search provision
// creates.
const provisionCoverageIndex = "node-pages-1"

// TestSearchRuntimeAuditProvisionCoverage runs ops search provision against
// OpenSearch under a new FoundationDB key prefix that does not record a
// serving index. The command must record its attempt and each model, index,
// and alias step attempt. The test deletes the provisioned index before and
// after the run.
func TestSearchRuntimeAuditProvisionCoverage(t *testing.T) {
	engine := testenv.OpenSearchWithMemory(t, cohortEngineMemoryBytes)
	integration.ConfigureSearchMaintenance(t, engine)
	t.Setenv("OPENSEARCH_SHARDS", "1")
	t.Setenv("OPENSEARCH_ROUTING_SHARDS", "24")
	t.Setenv("OPENSEARCH_REPLICAS", "0")
	clusterFile := testenv.FoundationDB(t)
	prefix := append([]byte("tack-test:"), []byte(uuid.NewString())...)
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() { clearReadPrefix(t, clusterFile, prefix) })
	cfg := provisionCoverageConfig(t, engine, clusterFile)
	deleteProvisionedIndex(t, engine)
	t.Cleanup(func() { deleteProvisionedIndex(t, engine) })
	pool := ledgerAdminPool(t)
	operatorID := uuid.Must(uuid.NewV7())
	if output, err := runOpsCommand(
		t,
		cfg,
		pool,
		"--execute",
		"--operator-id",
		operatorID.String(),
		"--operator-email",
		outcomeOperatorEmail,
		"ops",
		"search",
		"provision",
	); err != nil {
		t.Fatalf(
			"ops search provision failed; the test requires a successful run on an engine without the serving index: %v\n%s",
			err,
			output,
		)
	}
	events := outboxEventsByQuery(
		t,
		pool,
		"runtime_audit_verb_outbox.sql",
		string(audit.VerbOpsSearchProvision),
		operatorID.String(),
	)
	attempts := eventsInPhase(events, audit.PhaseAttempt, uuid.Nil)
	if len(attempts) != 1 {
		t.Fatalf("ops search provision recorded %d command attempts instead of 1", len(attempts))
	}
	commandTarget := attempts[0].Context.Target
	if commandTarget == nil || commandTarget.Kind != audit.TargetKindExternal ||
		commandTarget.Reference != "opensearch/serving-index" {
		t.Fatalf("ops search provision recorded command target %+v", commandTarget)
	}
	operationID := attempts[0].Context.OperationID
	stepAttempts := eventsInPhase(events, audit.PhaseAttempt, operationID)
	if len(stepAttempts) != 3 {
		t.Fatalf(
			"ops search provision recorded %d step attempts instead of 3 for the model, index, and alias steps",
			len(stepAttempts),
		)
	}
	wantTargets := map[string]bool{
		"opensearch/model/" + search.PinnedModel.Name: false,
		"opensearch/index/" + provisionCoverageIndex:  false,
		"opensearch/alias/" + search.PublicAlias:      false,
	}
	for _, attempt := range append(attempts, stepAttempts...) {
		if attempt.Outcome != audit.OutcomeAttempted || attempt.Actor.ID != operatorID {
			t.Fatalf(
				"ops search provision recorded attempt %s with actor %s and outcome %s",
				attempt.EventID,
				attempt.Actor.ID,
				attempt.Outcome,
			)
		}
		if attempt.Context.ParentOperationID == uuid.Nil {
			continue
		}
		if attempt.Context.Target == nil {
			t.Fatalf("step attempt %s does not record a target", attempt.EventID)
		}
		reference := attempt.Context.Target.Reference
		if _, found := wantTargets[reference]; !found || wantTargets[reference] {
			t.Fatalf("unexpected or repeated step target %q", reference)
		}
		wantTargets[reference] = true
	}
}

// provisionCoverageConfig returns the OpenSearch, ledger, and FoundationDB
// settings that ops search provision reads.
func provisionCoverageConfig(
	t *testing.T,
	engine testenv.OpenSearchFixture,
	clusterFile string,
) *config.Config {
	t.Helper()
	caPath := filepath.Join(t.TempDir(), "opensearch-ca.pem")
	if err := os.WriteFile(caPath, []byte(engine.CA), 0o600); err != nil {
		t.Fatalf(
			"Writing the OpenSearch CA file failed; the provision command requires it: %v",
			err,
		)
	}
	return &config.Config{
		SearchEndpoint:        engine.Endpoint,
		SearchCA:              caPath,
		SearchUsername:        engine.Username,
		SearchPassword:        engine.Password,
		SearchRequestTimeout:  2 * time.Minute,
		SearchMaxRetries:      1,
		DatabaseURL:           testenv.Ledger(t),
		FDBClusterFile:        clusterFile,
		FDBTransactionTimeout: 5 * time.Second,
	}
}

// deleteProvisionedIndex deletes provisionCoverageIndex and its aliases. A
// missing index satisfies the request.
func deleteProvisionedIndex(t *testing.T, engine testenv.OpenSearchFixture) {
	t.Helper()
	ctx := context.WithoutCancel(integration.WithTestOperation(t, t.Context()))
	adapter, err := search.New(
		ctx,
		search.Config{
			Endpoint:       engine.Endpoint,
			CA:             engine.CA,
			Username:       engine.Username,
			Password:       engine.Password,
			RequestTimeout: 2 * time.Minute,
			MaxRetries:     1,
		},
	)
	if err != nil {
		t.Fatalf(
			"Creating the OpenSearch adapter failed; the test requires it to delete %s: %v",
			provisionCoverageIndex,
			err,
		)
	}
	defer func() { _ = adapter.Close(ctx) }()
	if err := adapter.DeleteIndex(ctx, provisionCoverageIndex); err != nil {
		t.Fatalf("Deleting %s failed: %v", provisionCoverageIndex, err)
	}
}
