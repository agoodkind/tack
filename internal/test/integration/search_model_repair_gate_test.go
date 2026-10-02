package integration

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
)

const (
	// repairGateInterval is the repair check interval of the gate test.
	repairGateInterval = time.Second
	// repairGateQuietPeriod covers six check intervals.
	repairGateQuietPeriod = 6 * repairGateInterval
)

// TestSearchClusterModelRepairGates turns native automatic redeploy off and
// restarts the only member. The pinned model is then stuck, and only Tack can
// create a later deploy task. A Tack process with the repair disabled and a
// process with public search disabled each create no deploy task over six
// check intervals, read from the real task index. A process with both
// enabled creates one. The test covers the two stop boundaries only; it does
// not wait for the model to return to DEPLOYED.
func TestSearchClusterModelRepairGates(t *testing.T) {
	t.Setenv(repairEnabledVariable, "false")
	t.Setenv("OPENSEARCH_MODEL_REPAIR_INTERVAL", repairGateInterval.String())
	t.Setenv("OPENSEARCH_MODEL_REPAIR_STUCK_AFTER", "1s")
	t.Setenv("OPENSEARCH_MODEL_REPAIR_ATTEMPT_SPACING", "2s")
	cluster := startSearchTestCluster(t, 1)
	fixture, spec := newClusterQueryFixture(t, cluster)
	defer captureClusterTestFailure(t, cluster, fixture, spec.Model.ID)
	disableNativeRedeploy(t, fixture)
	member := cluster.Members()[0]
	cluster.StopMember(t, member)
	cluster.StartMember(t, member)
	clusterEventually(t, "leave the model stuck with no deploy task", func() error {
		deployment, err := fixture.Adapter.ModelDeployment(t.Context(), fixture.Index)
		if err != nil {
			return clusterFailure("read model deployment", err)
		}
		if !deployment.Stuck() {
			return fmt.Errorf("model state %s with %d active deploy tasks", deployment.State, deployment.ActiveTasks)
		}
		return nil
	})

	requireNoRepairDeploy(t, fixture, spec.Model.ID, "repair disabled", fixture.Config)
	t.Setenv(repairEnabledVariable, "true")
	withoutPublicSearch := *fixture.Config
	withoutPublicSearch.SearchPublicEnabled = false
	requireNoRepairDeploy(t, fixture, spec.Model.ID, "public search disabled", &withoutPublicSearch)

	since := clock.Now().UTC()
	buildRepairingQueryGraph(t, fixture.Config)
	clusterEventually(t, "create a repair deploy task with the repair and public search enabled", func() error {
		if deployTasksSince(t, fixture, spec.Model.ID, since) == 0 {
			return errors.New("the task index has no deploy task from the repair")
		}
		return nil
	})
}

// requireNoRepairDeploy starts one Tack process from cfg with its repair loop
// and requires that the task index records no deploy task of modelID during
// the quiet period. The model must still be stuck afterward.
func requireNoRepairDeploy(t *testing.T, fixture queryFixture, modelID, phase string, cfg *config.Config) {
	t.Helper()
	since := clock.Now().UTC()
	buildRepairingQueryGraph(t, cfg)
	time.Sleep(repairGateQuietPeriod)
	if count := deployTasksSince(t, fixture, modelID, since); count != 0 {
		t.Fatalf("%s: the task index records %d deploy tasks of model %s since %s, want none", phase, count, modelID, since)
	}
	deployment, err := fixture.Adapter.ModelDeployment(t.Context(), fixture.Index)
	if err != nil || !deployment.Stuck() {
		t.Fatalf("%s: model deployment %+v err %v after the quiet period, want it still stuck", phase, deployment, err)
	}
}
