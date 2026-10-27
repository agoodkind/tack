//go:build integration

package testenv_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"golang.org/x/sys/unix"

	"goodkind.io/tack/internal/testenv"
)

const (
	// engineChildVariable enables TestEngineChild when its environment value
	// is not empty.
	engineChildVariable = "TACK_TESTENV_ENGINE_CHILD"
	// engineChildReady is the prefix of the line that TestEngineChild prints after
	// its engine starts. The rest of the line is the engine container name.
	engineChildReady = "testenv engine child ready: "
	// engineChildLifetime bounds how long TestEngineChild waits for the parent
	// test to stop it.
	engineChildLifetime = 10 * time.Minute
)

// engineChild records a child test process and the name of the engine container
// it started.
type engineChild struct {
	command   *exec.Cmd
	container string
}

// TestEngineChild skips unless engineChildVariable has a nonempty environment
// value. It starts a FoundationDB engine, prints engineChildReady followed by the
// engine container name, and waits for a signal from the parent test.
func TestEngineChild(t *testing.T) {
	if os.Getenv(engineChildVariable) == "" {
		t.Skip("runs only as a child process of TestReleaseKeepsEnginesOfRunningProcesses")
	}
	clusterFile := testenv.FoundationDB(t)
	_, _ = fmt.Println(engineChildReady + testenv.FoundationDBContainer(clusterFile))
	time.Sleep(engineChildLifetime)
	t.Error("the parent test did not stop this process")
}

// TestReleaseKeepsEnginesOfRunningProcesses starts two children with separate
// FoundationDB engines and checks that SIGTERM removes only the first engine.
// It checks that SIGKILL leaves the second engine until a third child's first
// engine start removes it. It then checks that SIGINT removes the third engine.
func TestReleaseKeepsEnginesOfRunningProcesses(t *testing.T) {
	testenv.RequireDocker(t)
	first := startEngineChild(t)
	second := startEngineChild(t)
	stopEngineChild(t, first, unix.SIGTERM)
	requireEngine(t, first, false)
	requireEngine(t, second, true)
	stopEngineChild(t, second, unix.SIGKILL)
	requireEngine(t, second, true)
	third := startEngineChild(t)
	requireEngine(t, second, false)
	requireEngine(t, third, true)
	stopEngineChild(t, third, unix.SIGINT)
	requireEngine(t, third, false)
}

// startEngineChild starts this test binary as a child that runs TestEngineChild
// and returns after the child reports its engine container name. It registers
// cleanup that sends SIGTERM if the child still runs.
func startEngineChild(t *testing.T) engineChild {
	t.Helper()
	command := exec.CommandContext(context.WithoutCancel(t.Context()), os.Args[0], "-test.run=^TestEngineChild$", "-test.v")
	command.Env = append(os.Environ(), engineChildVariable+"=1")
	command.Stderr = os.Stderr
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("connect to the engine child output: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start an engine child: %v", err)
	}
	child := engineChild{command: command, container: ""}
	t.Cleanup(func() { stopEngineChild(t, child, unix.SIGTERM) })
	reported := make(chan string, 1)
	go func() {
		lines := bufio.NewScanner(output)
		container := ""
		for lines.Scan() {
			name, found := strings.CutPrefix(lines.Text(), engineChildReady)
			if found && container == "" {
				container = name
				reported <- container
			}
		}
		if container == "" {
			reported <- ""
		}
	}()
	child.container = <-reported
	if child.container == "" {
		t.Fatalf("engine child %d exited before it started an engine", command.Process.Pid)
	}
	return child
}

// stopEngineChild sends stop to the child if it still runs and waits for it to
// exit.
func stopEngineChild(t *testing.T, child engineChild, stop unix.Signal) {
	t.Helper()
	if child.command.ProcessState != nil {
		return
	}
	if err := child.command.Process.Signal(stop); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("send %v to engine child %d: %v", stop, child.command.Process.Pid, err)
	}
	_ = child.command.Wait()
}

// requireEngine fails the test unless the child's engine container exists
// exactly when present is true.
func requireEngine(t *testing.T, child engineChild, present bool) {
	t.Helper()
	cli, err := client.New(client.WithHost(client.DefaultDockerHost))
	if err != nil {
		t.Fatalf("build a Docker client: %v", err)
	}
	defer func() { _ = cli.Close() }()
	_, err = cli.ContainerInspect(t.Context(), child.container, client.ContainerInspectOptions{Size: false})
	switch {
	case err == nil && !present:
		t.Fatalf("engine %s of child %d still exists", child.container, child.command.Process.Pid)
	case cerrdefs.IsNotFound(err) && present:
		t.Fatalf("engine %s of child %d is gone", child.container, child.command.Process.Pid)
	case err != nil && !cerrdefs.IsNotFound(err):
		t.Fatalf("inspect engine %s: %v", child.container, err)
	}
}
