package integration_test

import (
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// staleRecordLag matches the delay between task completion and the model
	// record update in the engine log of a failed fixture.
	staleRecordLag      = 6 * time.Second
	staleRecordState    = "DEPLOY_FAILED"
	deployedRecordState = "DEPLOYED"
	// staleRecordForever keeps the stale state until the test ends.
	staleRecordForever = 24 * time.Hour
)

// TestSearchModelRecordLagAfterDeploy undeploys the pinned model and deploys it
// again on a real engine. A write block on the model index keeps the record in
// DEPLOY_FAILED, because it refuses the engine and sync-up writes, until
// staleRecordLag after the deploy task completes. Provision must wait and return
// the model.
func TestSearchModelRecordLagAfterDeploy(t *testing.T) {
	fixture, modelID := undeployedRecordFixture(t)
	startStaleRecord(t, fixture, modelID, staleRecordLag)
	ctx := integration.WithTestOperation(t, t.Context())

	model, err := fixture.Adapter.Provision(ctx)
	if err != nil {
		t.Fatalf("provision with a late model record: %v", err)
	}
	if model.ID != modelID {
		t.Fatalf("provision returned model %s, want %s", model.ID, modelID)
	}
	deployment, err := fixture.Adapter.ModelDeployment(ctx, fixture.Index, clock.Now())
	if err != nil {
		t.Fatalf("read model deployment: %v", err)
	}
	if deployment.State != deployedRecordState {
		t.Fatalf("model state is %s after provision, want %s", deployment.State, deployedRecordState)
	}
}

// TestSearchModelRecordStaysFailedAfterDeploy keeps the write block until the
// test ends. The record stays in DEPLOY_FAILED after the deploy task completes,
// and Provision must return the state mismatch.
func TestSearchModelRecordStaysFailedAfterDeploy(t *testing.T) {
	fixture, modelID := undeployedRecordFixture(t)
	startStaleRecord(t, fixture, modelID, staleRecordForever)

	_, err := fixture.Adapter.Provision(integration.WithTestOperation(t, t.Context()))
	if err == nil {
		t.Fatal("provision accepted a model record that stayed in DEPLOY_FAILED")
	}
	if !strings.Contains(err.Error(), staleRecordState) {
		t.Fatalf("provision error %q does not report state %s", err, staleRecordState)
	}
}

// undeployedRecordFixture returns a fixture with its pinned model undeployed.
// The fixture redeploys the model when the test ends.
func undeployedRecordFixture(t *testing.T) (integration.QueryFixture, string) {
	t.Helper()
	fixture := integration.NewQueryFixture(t)
	served, err := fixture.Adapter.ModelDeployment(integration.WithTestOperation(t, t.Context()), fixture.Index, clock.Now())
	if err != nil {
		t.Fatalf("read serving model: %v", err)
	}
	integration.UndeployNativeModel(t, fixture.Adapter, fixture.Client, served.ModelID)
	return fixture, served.ModelID
}
