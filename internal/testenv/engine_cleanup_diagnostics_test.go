//go:build integration

package testenv_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

const diagnosticRunLabel = "io.goodkind.tack.testenv.run"

// TestReleaseReportsAbnormalFixtures checks public cleanup with a failed broker,
// an oversized failure log, a normally completed helper, and a separate owner.
func TestReleaseReportsAbnormalFixtures(t *testing.T) {
	_ = testenv.Kafka(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
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
	broker := diagnosticBroker(t, cli)
	separate := maps.Clone(broker.Labels)
	separate[diagnosticRunLabel] += "-separate"
	unrelated := diagnosticHelper(t, cli, broker.Image, separate, []string{"sleep"}, []string{"300"})
	normal := diagnosticHelper(t, cli, broker.Image, broker.Labels, []string{"printf"}, []string{"normal helper completed\n"})
	failed := diagnosticHelper(t, cli, broker.Image, broker.Labels, []string{"java"}, []string{"-" + strings.Repeat("x", 32<<10)})
	waitForDiagnosticExit(t, cli, normal)
	waitForDiagnosticExit(t, cli, failed)
	if _, err := cli.ContainerKill(ctx, broker.ID, client.ContainerKillOptions{Signal: "SIGKILL"}); err != nil {
		t.Fatal(err)
	}
	waitForDiagnosticExit(t, cli, broker.ID)
	inspection, err := cli.ContainerInspect(ctx, broker.ID, client.ContainerInspectOptions{Size: false})
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
		t.Fatalf("cleanup reported %d abnormal fixtures, want the failed broker and helper", len(diagnostics))
	}
	for _, expected := range []string{"status exited", "exit code 137", "OOM killed false", `error ""`, "finished at " + state.FinishedAt, "last 80 engine log lines:\n"} {
		if !strings.Contains(diagnostics[broker.ID], expected) {
			t.Errorf("broker diagnostic omitted %q: %s", expected, diagnostics[broker.ID])
		}
	}
	if !strings.Contains(diagnostics[failed], "[older log bytes omitted]") || !strings.Contains(diagnostics[failed], "Could not create the Java Virtual Machine") {
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
	inspection, err = cli.ContainerInspect(ctx, unrelated, client.ContainerInspectOptions{Size: false})
	if err != nil || inspection.Container.State == nil || !inspection.Container.State.Running {
		t.Fatalf("Release changed the separate synthetic container %s: %v", unrelated, err)
	}
}

func diagnosticBroker(t *testing.T, cli *client.Client) container.Summary {
	t.Helper()
	listed, err := cli.ContainerList(t.Context(), client.ContainerListOptions{
		All: true, Filters: client.Filters{}.Add("label", "io.goodkind.tack.testenv.process="+strconv.Itoa(os.Getpid())),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range listed.Items {
		for _, name := range item.Names {
			if strings.HasPrefix(name, "/tack-testenv-kafka-") && !strings.HasPrefix(name, "/tack-testenv-kafka-address-") {
				return item
			}
		}
	}
	t.Fatal("Kafka did not start its broker container")
	return container.Summary{}
}

func diagnosticHelper(t *testing.T, cli *client.Client, image string, labels map[string]string, entrypoint, command []string) string {
	t.Helper()
	created, err := cli.ContainerCreate(t.Context(), client.ContainerCreateOptions{
		Config: &container.Config{Image: image, Entrypoint: entrypoint, Cmd: command, Labels: labels},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := cli.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		if err != nil && !cerrdefs.IsNotFound(err) {
			t.Error(err)
		}
	})
	if _, err := cli.ContainerStart(t.Context(), created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	return created.ID
}

func waitForDiagnosticExit(t *testing.T, cli *client.Client, identifier string) {
	t.Helper()
	waiting := cli.ContainerWait(t.Context(), identifier, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case <-waiting.Result:
	case err := <-waiting.Error:
		t.Fatal(err)
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
}

func readCleanupDiagnostics(t *testing.T, output []byte) map[string]string {
	t.Helper()
	diagnostics := make(map[string]string)
	lines := bufio.NewScanner(bytes.NewReader(output))
	lines.Buffer(make([]byte, 1024), 128<<10)
	for lines.Scan() {
		var record struct {
			Message     string `json:"msg"`
			Container   string `json:"container"`
			Diagnostics string `json:"err"`
		}
		if err := json.Unmarshal(lines.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record.Message == "testenv.engine.exited" {
			diagnostics[record.Container] = record.Diagnostics
		}
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	return diagnostics
}
