//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerDockerFixturePartialOwner(t *testing.T) {
	options, docker, _ := macOwnershipFixture(t, "PartialOwner")
	type outcome struct {
		result testenv.MacTestResult
		err    error
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	completed := make(chan outcome, 1)
	go func() { result, err := testenv.RunMacTests(ctx, options); completed <- outcome{result, err} }()
	finished := false
	t.Cleanup(func() {
		if !finished {
			cancel()
			select {
			case <-completed:
			case <-time.After(2 * time.Minute):
				t.Error("partial owner runner did not finish")
			}
		}
	})
	identity := regexp.MustCompile(`MAC_PARTIAL_OWNER_ID=([0-9a-f]{64}) PID=([0-9]+)`)
	deadline, stop := context.WithTimeout(t.Context(), 3*time.Minute)
	defer stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var match []string
	for len(match) == 0 {
		select {
		case <-ticker.C:
			contents, err := os.ReadFile(filepath.Join(options.EvidenceDirectory, "runtime.log"))
			if err == nil {
				match = identity.FindStringSubmatch(string(contents))
			}
			if len(match) != 0 && !macHasCreateEvent(options.EvidenceDirectory, match[1]) {
				match = nil
			}
		case <-deadline.Done():
			t.Fatalf("partial owner fixture did not record its create event: %v", deadline.Err())
		}
	}
	outer := strings.TrimSpace(macRead(t, options.EvidenceDirectory, "outer-id.txt"))
	if _, err := macFixtureExec(t.Context(), docker, outer, []string{"kill", "-KILL", match[2]}); err != nil {
		t.Fatal(err)
	}
	result := <-completed
	finished = true
	if result.err != nil || result.result.ExitCode != 1 {
		t.Errorf("partial owner runner: result=%+v error=%v", result.result, result.err)
	}
	summary := macOwnershipEvidence(t, options.EvidenceDirectory)
	fixture, found := summary.Fixtures.Containers[match[1]]
	if !found || !fixture.Create || !fixture.Log || !fixture.Absent || fixture.Labels["io.goodkind.tack.testenv.runner"] != "" || !summary.FullCleanup {
		t.Errorf("partial owner attribution or cleanup incomplete: %+v", summary)
	}
	if _, err := docker.ContainerInspect(t.Context(), match[1], client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Errorf("partial owner container remains: %v", err)
	}
	for name := range summary.Fixtures.Volumes {
		if _, err := docker.VolumeInspect(t.Context(), name, client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
			t.Errorf("partial owner volume remains %s: %v", name, err)
		}
	}
}

func macHasCreateEvent(directory, id string) bool {
	contents, err := os.ReadFile(filepath.Join(directory, "events.jsonl"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(contents), "\n") {
		var event events.Message
		if json.Unmarshal([]byte(line), &event) == nil && event.Type == events.ContainerEventType && event.Action == events.ActionCreate && event.Actor.ID == id {
			return true
		}
	}
	return false
}
