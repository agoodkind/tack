//go:build integration

package testenv_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/twmb/franz-go/pkg/kgo"
	"golang.org/x/sys/unix"

	"goodkind.io/tack/internal/testenv"
)

const (
	diagnosticChildVariable  = "TACK_TESTENV_RELEASE_DIAGNOSTIC_CHILD"
	diagnosticParentVariable = "TACK_TESTENV_RELEASE_DIAGNOSTIC_PARENT_KAFKA"
)

// TestReleaseReportsAbnormalFixtures runs destructive cleanup in a native child
// and verifies that the parent broker still answers after the child exits.
func TestReleaseReportsAbnormalFixtures(t *testing.T) {
	if os.Getenv(diagnosticChildVariable) == "1" {
		exerciseReleaseDiagnostics(t)
		return
	}
	bootstrap := testenv.Kafka(t)
	command := exec.CommandContext(
		t.Context(),
		os.Args[0],
		"-test.run=^TestReleaseReportsAbnormalFixtures$",
		"-test.timeout=2m",
		"-test.v",
	)
	command.Env = append(
		os.Environ(),
		diagnosticChildVariable+"=1",
		diagnosticParentVariable+"="+bootstrap,
	)
	command.Stdout, command.Stderr = t.Output(), t.Output()
	command.SysProcAttr = &unix.SysProcAttr{Setpgid: true}
	if err := command.Run(); err != nil {
		t.Fatalf("run the isolated Release regression: %v", err)
	}
	probe, err := kgo.NewClient(kgo.SeedBrokers(bootstrap))
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := probe.Ping(ctx); err != nil {
		t.Fatalf("parent Kafka fixture stopped answering after child cleanup: %v", err)
	}
}

func exerciseReleaseDiagnostics(t *testing.T) {
	t.Helper()
	parentBootstrap := os.Getenv(diagnosticParentVariable)
	if parentBootstrap == "" {
		t.Fatal("the isolated Release regression requires its parent Kafka bootstrap")
	}
	bootstrap := testenv.Kafka(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cli, err := client.New(client.WithHost(client.DefaultDockerHost))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	t.Cleanup(func() {
		if err := testenv.Release(context.Background()); err != nil {
			t.Error(err)
		}
	})
	broker := diagnosticBroker(t, cli, bootstrap)
	competing := diagnosticBroker(t, cli, parentBootstrap)
	if broker.ID == competing.ID {
		t.Fatal(
			"broker discovery selected one container for distinct parent and child bootstrap addresses",
		)
	}
	normal := diagnosticHelper(
		t,
		cli,
		broker.Image,
		broker.Labels,
		[]string{"printf"},
		[]string{"normal helper completed\n"},
	)
	failed := diagnosticHelper(
		t,
		cli,
		broker.Image,
		broker.Labels,
		[]string{"java"},
		[]string{"-" + strings.Repeat("x", 32<<10)},
	)
	waitForDiagnosticExit(t, cli, normal)
	waitForDiagnosticExit(t, cli, failed)
	if _, err := cli.ContainerKill(
		ctx,
		broker.ID,
		client.ContainerKillOptions{Signal: "SIGKILL"},
	); err != nil {
		t.Fatal(err)
	}
	waitForDiagnosticExit(t, cli, broker.ID)
	inspection, err := cli.ContainerInspect(
		ctx,
		broker.ID,
		client.ContainerInspectOptions{Size: false},
	)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Container.State == nil {
		t.Fatal("Docker did not return the terminated broker state")
	}
	state := inspection.Container.State
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	if err := testenv.Release(ctx); err != nil {
		t.Fatal(err)
	}
	slog.SetDefault(previous)
	diagnostics := readCleanupDiagnostics(t, output.Bytes())
	if len(diagnostics) != 2 {
		t.Fatalf(
			"cleanup reported %d abnormal fixtures, want the failed broker and helper",
			len(diagnostics),
		)
	}
	for _, expected := range []string{
		"status exited",
		"exit code 137",
		"OOM killed false",
		`error ""`,
		"finished at " + state.FinishedAt,
		"last 80 engine log lines:\n",
	} {
		if !strings.Contains(diagnostics[broker.ID], expected) {
			t.Errorf("broker diagnostic omitted %q: %s", expected, diagnostics[broker.ID])
		}
	}
	if !strings.Contains(diagnostics[failed], "[older log bytes omitted]") ||
		!strings.Contains(diagnostics[failed], "Could not create the Java Virtual Machine") {
		t.Error("helper diagnostic did not retain its bounded final log")
	}
	if len(diagnostics[failed]) > 32<<10 || len(diagnostics[broker.ID]) > 32<<10 {
		t.Fatal("cleanup captured more than 32 KiB for a failed fixture")
	}
	if strings.Contains(output.String(), "normal helper completed") {
		t.Error("cleanup captured a normally completed helper's log")
	}
	for _, identifier := range []string{broker.ID, normal, failed} {
		_, err := cli.ContainerInspect(ctx, identifier, client.ContainerInspectOptions{Size: false})
		if !cerrdefs.IsNotFound(err) {
			t.Errorf("Release did not remove selected fixture %s: %v", identifier, err)
		}
	}
	inspection, err = cli.ContainerInspect(
		ctx,
		competing.ID,
		client.ContainerInspectOptions{Size: false},
	)
	if err != nil || inspection.Container.State == nil || !inspection.Container.State.Running {
		t.Fatalf("Release changed the parent broker %s: %v", competing.ID, err)
	}
}
