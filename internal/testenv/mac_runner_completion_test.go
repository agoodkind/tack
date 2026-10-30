//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

type macCorrectionSummary struct {
	RunnerID string `json:"runner_id"`
	Wait     *struct {
		Response         *container.WaitResponse `json:"response"`
		Error            string                  `json:"error"`
		ReceiverComplete bool                    `json:"receiver_complete"`
	} `json:"wait"`
	TemporaryVolumes map[string]struct {
		Name          string `json:"name"`
		IdentityError string `json:"identity_error"`
		Absent        bool   `json:"absent"`
	} `json:"temporary_volumes"`
	UnknownResources []string `json:"unknown_resources"`
}

func macCorrectionEvidence(t *testing.T, directory string) macCorrectionSummary {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(directory, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary macCorrectionSummary
	if err := json.Unmarshal(contents, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Wait == nil || !summary.Wait.ReceiverComplete {
		t.Fatalf("Docker wait receiver did not complete: %s", contents)
	}
	return summary
}

func TestMacRunnerDockerWaitEvidenceFailure(t *testing.T) {
	options, docker, _ := macDockerFixture(t)
	options.TestArguments = []string{"-run=^TestMacRunnerSignal$", "-count=1", "-timeout=3m", "-tags=macrunnerfixture,runtimeaudit"}
	blocked := make(chan error, 1)
	go func() { blocked <- macBlockStateEvidence(t.Context(), options.EvidenceDirectory) }()
	result, err := testenv.RunMacTests(t.Context(), options)
	if blockErr := <-blocked; blockErr != nil {
		t.Fatal(blockErr)
	}
	if err == nil || !strings.Contains(err.Error(), "states.jsonl") || result.ExitCode != 1 {
		t.Fatalf("state evidence failure did not preserve its primary error: result=%+v error=%v", result, err)
	}
	macRuntimeEvidence(t, options, docker)
	macCorrectionEvidence(t, options.EvidenceDirectory)
}

func macBlockStateEvidence(ctx context.Context, directory string) error {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_, err := os.Stat(directory)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(directory, "states.jsonl"), []byte("{\"fixture\":\"reserve the state evidence file before runtime\"}\n"), 0o600)
		case <-bounded.Done():
			return bounded.Err()
		}
	}
}

func TestMacRunnerDockerRemovedContainer(t *testing.T) {
	options, docker, project := macDockerFixture(t)
	options.TestArguments = []string{"-run=^TestMacRunnerSignal$", "-count=1", "-timeout=3m", "-tags=macrunnerfixture,runtimeaudit"}
	type completion struct {
		result testenv.MacTestResult
		err    error
	}
	completed := make(chan completion, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	received := false
	t.Cleanup(func() {
		if received {
			return
		}
		cancel()
		select {
		case <-completed:
		case <-time.After(2 * time.Minute):
			t.Error("fixture runner did not finish after cancellation")
		}
	})
	go func() { result, err := testenv.RunMacTests(ctx, options); completed <- completion{result, err} }()
	id := macWaitFixtureReady(t, options.EvidenceDirectory)
	inspection, err := docker.ContainerInspect(t.Context(), id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	configuration := inspection.Container.Config
	if configuration == nil || configuration.Labels["com.docker.compose.project"] != project || configuration.Labels["io.goodkind.tack.mac-runner.native"] != "true" || configuration.Labels["io.goodkind.tack.mac-runner.root"] != options.Root {
		t.Fatal("refuse to remove a container without exact fixture ownership")
	}
	var volume string
	for _, point := range inspection.Container.Mounts {
		if point.Destination == "/tmp/tack-test" && point.Type == mount.TypeVolume {
			volume = point.Name
		}
	}
	if volume == "" || volume == project+"_tack-test-go-cache" || volume == project+"_tack-test-go-mod" {
		t.Fatal("fixture did not observe a distinct temporary volume")
	}
	t.Cleanup(func() {
		if _, err := docker.VolumeRemove(context.Background(), volume, client.VolumeRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
			t.Error(err)
		}
	})
	if _, err := docker.ContainerRemove(t.Context(), id, client.ContainerRemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	intervention := fmt.Sprintf("{\"operation\":\"fixture container remove without volumes\",\"container\":%q,\"temporary_volume\":%q}\n", id, volume)
	if err := os.WriteFile(filepath.Join(options.EvidenceDirectory, "fixture-intervention.json"), []byte(intervention), 0o600); err != nil {
		t.Fatal(err)
	}
	completionResult := <-completed
	received = true
	if completionResult.err == nil || completionResult.result.ExitCode != 1 {
		t.Fatalf("external removal did not preserve the primary failure: result=%+v error=%v", completionResult.result, completionResult.err)
	}
	macRuntimeEvidence(t, options, docker)
	summary := macCorrectionEvidence(t, options.EvidenceDirectory)
	macRequireFinalLogFailure(t, options.EvidenceDirectory, id)
	recorded := summary.TemporaryVolumes[id]
	if recorded.Name != volume || !recorded.Absent || recorded.IdentityError != "" || len(summary.UnknownResources) != 0 {
		t.Fatalf("missing container did not require known temporary-volume absence: %+v", summary)
	}
}

func macWaitFixtureReady(t *testing.T, directory string) string {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			contents, err := os.ReadFile(filepath.Join(directory, "runtime.log"))
			if err != nil || !strings.Contains(string(contents), "MAC_RUNNER_SIGNAL_READY") {
				continue
			}
			identity, err := os.ReadFile(filepath.Join(directory, "outer-id.txt"))
			if err != nil {
				t.Fatal(err)
			}
			return strings.TrimSpace(string(identity))
		case <-timer.C:
			t.Fatal("fixture did not report actual runtime readiness")
		}
	}
}
