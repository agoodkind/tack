//go:build darwin && integration && macrunnerfixture

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

const (
	macCommandReadyLimit = 60 * time.Second
	macCommandExitLimit  = 6 * time.Minute
)

// TestMacRunnerCommandSignals runs the run-mac command in this process and sends
// each signal to this process after the fixture reports readiness. The command
// must return status 130 and leave complete evidence and cleanup.
func TestMacRunnerCommandSignals(t *testing.T) {
	for _, delivered := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(strconv.Itoa(int(delivered)), func(t *testing.T) {
			options, docker := macCommandFixture(t)
			options.TestArguments = []string{"-run=^TestMacRunnerSignal$", "-count=1", "-timeout=3m", "-tags=macrunnerfixture,runtimeaudit"}
			finished := macCommandStart(options)
			macCommandReady(t, finished, options.EvidenceDirectory)
			if err := syscall.Kill(os.Getpid(), delivered); err != nil {
				t.Fatal(err)
			}
			macCommandExit(t, finished, 130)
			macCommandEvidence(t, docker, options.EvidenceDirectory, 130, true)
		})
	}
}

// TestMacRunnerCommandNonzero runs the run-mac command with a failing fixture
// test. The command must return the test's status.
func TestMacRunnerCommandNonzero(t *testing.T) {
	options, docker := macCommandFixture(t)
	options.TestArguments = []string{"-run=^TestMacRunnerFailure$", "-count=1", "-timeout=45s", "-tags=macrunnerfixture,runtimeaudit"}
	finished := macCommandStart(options)
	macCommandExit(t, finished, 1)
	macCommandEvidence(t, docker, options.EvidenceDirectory, 1, false)
}

// macCommandStart runs the run-mac command in a goroutine and returns the channel
// that receives its exit status.
func macCommandStart(options testenv.MacTestOptions) <-chan int {
	arguments := []string{"run-mac", "--root", options.Root, "--image", options.Image, "--evidence-dir", options.EvidenceDirectory, "--"}
	arguments = append(arguments, options.TestArguments...)
	finished := make(chan int, 1)
	go func() { finished <- run(arguments) }()
	return finished
}

func macCommandReady(t *testing.T, finished <-chan int, directory string) {
	t.Helper()
	timer := time.NewTimer(macCommandReadyLimit)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			contents, err := os.ReadFile(filepath.Join(directory, "runtime.log"))
			if err == nil && strings.Contains(string(contents), "MAC_RUNNER_SIGNAL_READY") {
				return
			}
		case code := <-finished:
			t.Fatalf("public command returned %d before the fixture reported readiness", code)
		case <-timer.C:
			t.Fatal("public command did not report actual fixture readiness")
		}
	}
}

func macCommandExit(t *testing.T, finished <-chan int, expected int) {
	t.Helper()
	timer := time.NewTimer(macCommandExitLimit)
	defer timer.Stop()
	select {
	case code := <-finished:
		if code != expected {
			t.Fatalf("public command status: got=%d expected=%d", code, expected)
		}
	case <-timer.C:
		t.Fatalf("public command did not return within %s", macCommandExitLimit)
	}
}

func macCommandEvidence(t *testing.T, docker *client.Client, directory string, expected int, interrupted bool) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(directory, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		RunnerID         string          `json:"runner_id"`
		RuntimeExit      *int            `json:"runtime_exit"`
		Error            string          `json:"error"`
		ExitCode         int             `json:"exit_code"`
		Interrupted      bool            `json:"interrupted"`
		AttachComplete   bool            `json:"attach_complete"`
		EventsComplete   bool            `json:"events_complete"`
		AnonymousVolumes map[string]bool `json:"anonymous_volumes"`
		Wait             *struct {
			ReceiverComplete bool `json:"receiver_complete"`
		} `json:"wait"`
	}
	if err := json.Unmarshal(contents, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.ExitCode != expected || summary.Interrupted != interrupted || !summary.AttachComplete || !summary.EventsComplete || summary.RunnerID == "" {
		t.Fatalf("public command evidence: %s", contents)
	}
	if summary.Wait == nil || !summary.Wait.ReceiverComplete {
		t.Fatalf("public command did not complete its Docker wait receiver: %s", contents)
	}
	if !interrupted && (summary.Error != "" || summary.RuntimeExit == nil || *summary.RuntimeExit != expected) {
		t.Fatalf("public command did not preserve the actual test exit without an orchestration error: %s", contents)
	}
	for _, name := range []string{"cleanup.exit", "source-hash-verification.exit"} {
		status, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || string(status) != "0\n" {
			t.Fatalf("public command finalization %s: contents=%q error=%v", name, status, err)
		}
	}
	if _, err := docker.ContainerInspect(t.Context(), summary.RunnerID, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatalf("public command retained runner %s: %v", summary.RunnerID, err)
	}
	for name := range summary.AnonymousVolumes {
		if _, err := docker.VolumeInspect(t.Context(), name, client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
			t.Fatalf("public command retained temporary volume %s: %v", name, err)
		}
	}
	t.Logf("private command evidence: %s", directory)
}
