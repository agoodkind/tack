//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

type macOwnershipSummary struct {
	RunnerID    string `json:"runner_id"`
	ExitCode    int    `json:"exit_code"`
	Attribution bool   `json:"fixture_attribution_complete"`
	Cleanup     bool   `json:"fixture_cleanup_complete"`
	FullCleanup bool   `json:"full_cleanup_complete"`
	Fixtures    struct {
		Containers map[string]struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
			Create bool              `json:"create_observed"`
			Log    bool              `json:"log_observed"`
			Absent bool              `json:"absent"`
		} `json:"containers"`
		Networks map[string]struct {
			Absent bool `json:"absent"`
		} `json:"networks"`
		Volumes   map[string]bool `json:"volume_candidates"`
		Preserved map[string]struct {
			ID         string `json:"id"`
			Kind       string `json:"kind"`
			Incomplete bool   `json:"incomplete"`
		} `json:"preserved"`
	} `json:"fixtures"`
}

func macOwnershipEvidence(t *testing.T, directory string) macOwnershipSummary {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(directory, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary macOwnershipSummary
	if err := json.Unmarshal(contents, &summary); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fixture-ids.json", "inner-process-ids.json", "unattributed-preserved-ids.json", "fixture-attribution.exit", "fixture-cleanup.exit"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("private ownership evidence: %s", directory)
	return summary
}

func macRequireOwnedAbsent(t *testing.T, docker *client.Client, summary macOwnershipSummary) {
	t.Helper()
	if len(summary.Fixtures.Containers) < 7 || len(summary.Fixtures.Networks) != 1 {
		t.Fatalf("fixture inventory omitted a helper: %+v", summary.Fixtures)
	}
	logged := 0
	for id, fixture := range summary.Fixtures.Containers {
		if fixture.Log {
			logged++
		}
		if fixture.Labels["io.goodkind.tack.testenv.runner"] != summary.RunnerID || !fixture.Absent || !fixture.Create {
			t.Errorf("fixture lacks exact owner/create/absence evidence: %s: %+v", id, fixture)
		}
		if _, err := docker.ContainerInspect(t.Context(), id, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
			t.Errorf("owned fixture remains %s: %v", id, err)
		}
	}
	if logged < 6 {
		t.Errorf("engine/cluster/proxy logs did not cross-check every helper: %d", logged)
	}
	for id, fixture := range summary.Fixtures.Networks {
		if !fixture.Absent {
			t.Errorf("owned network lacks absence: %s", id)
		}
		if _, err := docker.NetworkInspect(t.Context(), id, client.NetworkInspectOptions{}); !errdefs.IsNotFound(err) {
			t.Errorf("owned network remains %s: %v", id, err)
		}
	}
}

func macOwnershipRecovery(t *testing.T, docker *client.Client, directory string) {
	t.Helper()
	t.Cleanup(func() {
		contents, err := os.ReadFile(filepath.Join(directory, "result.json"))
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Error(err)
			return
		}
		var summary macOwnershipSummary
		if err := json.Unmarshal(contents, &summary); err != nil {
			t.Error(err)
			return
		}
		logs, err := os.ReadFile(filepath.Join(directory, "runtime.log"))
		if err != nil {
			t.Error(err)
			return
		}
		var fixtureID string
		for _, line := range strings.Split(string(logs), "\n") {
			if value, present := strings.CutPrefix(line, "MAC_FIXTURE_VOLUME_CONTAINER="); present {
				fixtureID = strings.TrimSpace(value)
			}
		}
		for _, preserved := range summary.Fixtures.Preserved {
			if !preserved.Incomplete || preserved.Kind != "container" || preserved.ID != fixtureID {
				continue
			}
			inspection, err := docker.ContainerInspect(context.Background(), preserved.ID, client.ContainerInspectOptions{})
			if errdefs.IsNotFound(err) {
				continue
			}
			if err != nil || inspection.Container.Config == nil || inspection.Container.Config.Labels["io.goodkind.tack.testenv"] != "true" || inspection.Container.Config.Labels["io.goodkind.tack.testenv.runner"] != "" {
				t.Errorf("refuse preserved fixture recovery %s: %v", preserved.ID, err)
				continue
			}
			if _, err := docker.ContainerRemove(context.Background(), preserved.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
				t.Error(err)
			}
		}
	})
}
