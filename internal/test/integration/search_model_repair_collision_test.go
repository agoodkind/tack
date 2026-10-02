package integration

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

const (
	// collisionAttempts is two rounds of restarts over the three members.
	collisionAttempts = 6
	// collisionPoll is the task index poll interval after a restart.
	collisionPoll = 25 * time.Millisecond
	// collisionNativeWait bounds the wait for the native task after a restart.
	collisionNativeWait = time.Minute
)

// TestSearchClusterModelRepairCollision produces cause C on a 3-member cluster with
// native automatic redeploy enabled. Each attempt restarts the current
// cluster manager, which starts two native redeploy tasks: the new manager's
// arrangement and the node join. The test sends two concurrent deploys when
// the first native task appears. A collision
// is a FAILED task with a version conflict, a sibling task that read
// DEPLOY_FAILED, and a stuck model record. After the collision, the
// production repair loop must restore DEPLOYED on 3 of 3 members with 1 to 3
// repair tasks. Every later task must be a repair task.
func TestSearchClusterModelRepairCollision(t *testing.T) {
	t.Setenv(repairEnabledVariable, "true")
	repairTasks := recordRepairTasks(t)
	cluster := startSearchTestCluster(t, 3)
	cluster.AddMember(t)
	cluster.AddMember(t)
	members := cluster.Members()
	fixture, spec := newClusterQueryFixture(t, cluster)
	defer captureClusterTestFailure(t, cluster, fixture, spec.Model.ID)
	modelID := spec.Model.ID
	requireModelOnEveryMember(t, fixture, modelID, len(members))
	requireClusterPredictors(t, cluster, fixture, modelID)

	for attempt := range collisionAttempts {
		member := clusterManagerMember(t, fixture)
		repairsBefore := len(repairTasks())
		restarted := clock.Now().UTC()
		cluster.StopMember(t, member)
		cluster.StartMember(t, member)
		awaitDeployTask(t, fixture, modelID, restarted)
		fault := injectDeployPair(t, fixture, modelID)
		tasks := withoutTasks(awaitTasksEnded(t, fixture, modelID, restarted), repairTasks()[repairsBefore:])
		record := modelRecord(t, fixture, modelID)
		for _, task := range tasks {
			t.Logf("attempt=%d member=%s task id=%s created=%s state=%s fault=%t error=%q", attempt+1, member,
				task.ID, task.Created.Format(time.RFC3339Nano), task.State, slices.Contains(fault, task.ID), task.Error)
		}
		t.Logf("attempt=%d model state=%s workers=%d of %d", attempt+1, record.State, record.Current, record.Planned)
		if !collided(tasks, record) {
			requireClusterPredictors(t, cluster, fixture, modelID)
			continue
		}
		requireRepairedCollision(t, cluster, fixture, modelID, restarted, tasks, fault, repairsBefore, repairTasks)
		return
	}
	t.Fatalf("collision not produced in %d attempts", collisionAttempts)
}

// requireRepairedCollision waits for DEPLOYED on every member and attributes
// every task created since the restart to the collision, the injected
// faults, or the repair.
func requireRepairedCollision(t *testing.T, cluster *testenv.OpenSearchCluster, fixture queryFixture, modelID string,
	restarted time.Time, collision []deployTask, fault []string, repairsBefore int, repairTasks func() []string,
) {
	t.Helper()
	collisionIDs := make([]string, 0, len(collision))
	for _, task := range collision {
		collisionIDs = append(collisionIDs, task.ID)
	}
	clusterEventually(t, "restore DEPLOYED on every member after the collision", func() error {
		record := modelRecord(t, fixture, modelID)
		if record.State != "DEPLOYED" || record.Current != record.Planned || record.Planned != len(cluster.Members()) {
			return fmt.Errorf("model state %s with %d of %d workers", record.State, record.Current, record.Planned)
		}
		return nil
	})
	t.Logf("model DEPLOYED read at=%s", clock.Now().UTC().Format(time.RFC3339Nano))
	requireClusterPredictors(t, cluster, fixture, modelID)
	repairs := repairTasks()[repairsBefore:]
	if len(repairs) < 1 || len(repairs) > 3 {
		t.Fatalf("the repair sent %d deploy tasks %v after the collision, want 1 to 3", len(repairs), repairs)
	}
	for _, task := range deployTasksFrom(t, fixture, modelID, restarted) {
		role := "unattributed"
		switch {
		case slices.Contains(fault, task.ID):
			role = "fault"
		case slices.Contains(repairs, task.ID):
			role = "repair"
		case slices.Contains(collisionIDs, task.ID):
			role = "native"
		}
		t.Logf("after collision task id=%s created=%s state=%s role=%s error=%q", task.ID,
			task.Created.Format(time.RFC3339Nano), task.State, role, task.Error)
		if role == "unattributed" {
			t.Fatalf("task %s is neither a fault, a repair, nor a native task from the collision", task.ID)
		}
	}
	if _, err := trySearch(fixture.Harness, "collision repaired", ""); err != nil {
		t.Fatalf("public search after the repair: %v", err)
	}
}

// withoutTasks returns tasks without the tasks named in ids. A repair task
// created during the collision window is a repair, not a native task.
func withoutTasks(tasks []deployTask, ids []string) []deployTask {
	return slices.DeleteFunc(tasks, func(task deployTask) bool { return slices.Contains(ids, task.ID) })
}

// collided reports a FAILED version conflict task, a sibling that read
// DEPLOY_FAILED, and a stuck model record.
func collided(tasks []deployTask, record modelRecordState) bool {
	conflict := slices.ContainsFunc(tasks, func(task deployTask) bool {
		return task.State == "FAILED" && strings.Contains(task.Error, "version conflict, required seqNo")
	})
	sibling := slices.ContainsFunc(tasks, func(task deployTask) bool {
		return task.State == "COMPLETED_WITH_ERROR" && strings.Contains(task.Error, "but the model is in state: DEPLOY_FAILED")
	})
	stuck := record.State == "PARTIALLY_DEPLOYED" || record.State == "DEPLOY_FAILED"
	return conflict && sibling && stuck
}

// awaitDeployTask polls until the task index records a DEPLOY_MODEL task
// created at or after since, or until collisionNativeWait passes.
func awaitDeployTask(t *testing.T, fixture queryFixture, modelID string, since time.Time) {
	t.Helper()
	deadline := clock.Now().Add(collisionNativeWait)
	for len(deployTasksFrom(t, fixture, modelID, since)) == 0 && clock.Now().Before(deadline) {
		time.Sleep(collisionPoll)
	}
}

// awaitTasksEnded returns the tasks created at or after since once every
// one has ended.
func awaitTasksEnded(t *testing.T, fixture queryFixture, modelID string, since time.Time) []deployTask {
	t.Helper()
	var tasks []deployTask
	clusterEventually(t, "end every deploy task since the restart", func() error {
		tasks = deployTasksFrom(t, fixture, modelID, since)
		if index := slices.IndexFunc(tasks, func(task deployTask) bool { return !task.ended() }); index >= 0 {
			return fmt.Errorf("task %s is %s", tasks[index].ID, tasks[index].State)
		}
		return nil
	})
	return tasks
}

type modelRecordState struct {
	State   string `json:"model_state"`
	Current int    `json:"current_worker_node_count"`
	Planned int    `json:"planning_worker_node_count"`
}

func modelRecord(t *testing.T, fixture queryFixture, modelID string) modelRecordState {
	t.Helper()
	var record modelRecordState
	response, err := opensearch.Do(t.Context(), fixture.Client.Client, http.MethodGet, clusterModelRecordRequest{modelID: modelID}, &record)
	if err == nil && (response == nil || response.IsError()) {
		err = errors.New("model record request failed")
	}
	if err != nil {
		t.Fatalf("read model record %s: response %v err %v", modelID, response, err)
	}
	return record
}
