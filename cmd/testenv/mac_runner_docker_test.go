//go:build darwin && integration && macrunnerfixture

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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

const macRunnerChild = "TACK_MAC_RUNNER_COMMAND_CHILD"

func TestMacRunnerCommandChild(t *testing.T) {
	encoded := os.Getenv(macRunnerChild)
	if encoded == "" {
		return
	}
	var options testenv.MacTestOptions
	if err := json.Unmarshal([]byte(encoded), &options); err != nil {
		t.Fatal(err)
	}
	arguments := []string{"run-mac", "--root", options.Root, "--image", options.Image, "--evidence-dir", options.EvidenceDirectory, "--"}
	arguments = append(arguments, options.TestArguments...)
	os.Exit(run(arguments))
}

func TestMacRunnerCommandSignals(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(strconv.Itoa(int(signal)), func(t *testing.T) {
			options, docker := macCommandFixture(t)
			options.TestArguments = []string{"-run=^TestMacRunnerSignal$", "-count=1", "-timeout=3m", "-tags=macrunnerfixture,runtimeaudit"}
			command := macCommandStart(t, options)
			macCommandReady(t, command, options.EvidenceDirectory)
			if err := command.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			macCommandExit(t, command, 130)
			macCommandEvidence(t, docker, options.EvidenceDirectory, 130, true)
		})
	}
}

func TestMacRunnerCommandNonzero(t *testing.T) {
	options, docker := macCommandFixture(t)
	options.TestArguments = []string{"-run=^TestMacRunnerFailure$", "-count=1", "-timeout=45s", "-tags=macrunnerfixture,runtimeaudit"}
	command := macCommandStart(t, options)
	macCommandExit(t, command, 1)
	macCommandEvidence(t, docker, options.EvidenceDirectory, 1, false)
}

func macCommandStart(t *testing.T, options testenv.MacTestOptions) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, executable, "-test.run=^TestMacRunnerCommandChild$")
	command.Env = append(os.Environ(), macRunnerChild+"="+string(encoded))
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Signal(syscall.SIGTERM)
			_ = command.Wait()
		}
	})
	return command
}

func macCommandReady(t *testing.T, command *exec.Cmd, directory string) {
	t.Helper()
	timer := time.NewTimer(60 * time.Second)
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
		case <-timer.C:
			t.Fatalf("public command process %d did not report actual fixture readiness", command.Process.Pid)
		}
	}
}

func macCommandExit(t *testing.T, command *exec.Cmd, expected int) {
	t.Helper()
	err := command.Wait()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != expected {
		t.Fatalf("public command status: expected=%d error=%v", expected, err)
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
	}
	if err := json.Unmarshal(contents, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.ExitCode != expected || summary.Interrupted != interrupted || !summary.AttachComplete || !summary.EventsComplete || summary.RunnerID == "" {
		t.Fatalf("public command evidence: %s", contents)
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
