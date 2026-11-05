//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerDockerNonzero(t *testing.T) {
	options, docker, _ := macDockerFixture(t)
	options.TestArguments[0] = "-run=^TestMacRunnerFailure$"
	options.TestArguments[1] = "-count=1"
	result, err := testenv.RunMacTests(t.Context(), options)
	if err != nil || result.ExitCode != 1 {
		t.Fatalf("preserve test failure without orchestration error: result=%+v error=%v", result, err)
	}
	summary := macRuntimeEvidence(t, options, docker)
	if summary.RuntimeExit == nil || *summary.RuntimeExit != 1 || !summary.Started {
		t.Fatalf("missing authoritative failed-test status: %+v", summary)
	}
}

func TestMacRunnerDockerSourceMutation(t *testing.T) {
	options, docker, _ := macDockerFixture(t)
	options.TestArguments[0] = "-run=^TestMacRunnerMutation$"
	options.TestArguments[1] = "-count=1"
	result, err := testenv.RunMacTests(t.Context(), options)
	if err == nil || !strings.Contains(err.Error(), "source hash changed") || result.ExitCode != 1 {
		t.Fatalf("reject fixture source mutation: result=%+v error=%v", result, err)
	}
	summary := macRuntimeEvidence(t, options, docker)
	if summary.RuntimeExit == nil || *summary.RuntimeExit != 0 {
		t.Fatalf("source error replaced runtime exit: %+v", summary)
	}
	contents, err := os.ReadFile(filepath.Join(options.EvidenceDirectory, "source-hash-verification.exit"))
	if err != nil || string(contents) != "1\n" {
		t.Fatalf("source verification evidence: contents=%q error=%v", contents, err)
	}
	macRequireStageFailure(t, options.EvidenceDirectory, "source-hash-verification", "source hash changed")
}

func TestMacRunnerDockerAbsentImage(t *testing.T) {
	options, docker, _ := macDockerFixture(t)
	options.Image = macImage
	if _, err := docker.ImageInspect(t.Context(), options.Image); !errdefs.IsNotFound(err) {
		t.Fatalf("absent-image fixture unexpectedly exists: %v", err)
	}
	result, err := testenv.RunMacTests(t.Context(), options)
	if err == nil || !strings.Contains(err.Error(), "inspect required immutable local image") || result.RunnerID != "" || result.ExitCode != 1 {
		t.Fatalf("reject absent image: result=%+v error=%v", result, err)
	}
	summary := macRuntimeEvidence(t, options, docker)
	if summary.RuntimeExit != nil || summary.Started {
		t.Fatalf("absent image fabricated runtime: %+v", summary)
	}
	if _, err := os.Stat(filepath.Join(options.EvidenceDirectory, "runner-request.json")); !os.IsNotExist(err) {
		t.Fatalf("absent image attempted creation: %v", err)
	}
}

func TestMacRunnerDockerStartupFailure(t *testing.T) {
	options, docker, project := macDockerFixture(t)
	options.Image = macImportStartupImage(t, docker, project)
	result, err := testenv.RunMacTests(t.Context(), options)
	if err == nil || !strings.Contains(err.Error(), "start runner") || result.ExitCode != 1 || result.RunnerID == "" {
		t.Fatalf("record actual startup failure: result=%+v error=%v", result, err)
	}
	summary := macRuntimeEvidence(t, options, docker)
	if summary.Started || summary.RuntimeExit != nil || !summary.AttachComplete || !summary.EventsComplete {
		t.Fatalf("startup failure fabricated runtime status: %+v", summary)
	}
	if _, err := os.Stat(filepath.Join(options.EvidenceDirectory, "outer-terminal.json")); err != nil {
		t.Fatal(err)
	}
}

func macImportStartupImage(t *testing.T, docker *client.Client, project string) string {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "mac-runner-fixture", Mode: 0o600, Size: int64(len(project))}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, project); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	response, err := docker.ImageImport(t.Context(), client.ImageImportSource{Source: &archive, SourceName: "-"}, project+":startup", client.ImageImportOptions{
		Changes: []string{"LABEL tack.mac-runner-fixture=" + project},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Close() }()
	decoder := json.NewDecoder(response)
	for {
		var message struct {
			Error string `json:"error"`
		}
		if err := decoder.Decode(&message); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if message.Error != "" {
			t.Fatal(message.Error)
		}
	}
	inspection, err := docker.ImageInspect(t.Context(), project+":startup")
	if err != nil {
		t.Fatal(err)
	}
	id := inspection.ID
	t.Cleanup(func() {
		current, err := docker.ImageInspect(context.Background(), id)
		if err != nil || current.Config.Labels["tack.mac-runner-fixture"] != project {
			t.Errorf("refuse startup fixture image removal: %s: %v", id, err)
			return
		}
		if _, err := docker.ImageRemove(context.Background(), id, client.ImageRemoveOptions{}); err != nil {
			t.Error(err)
		}
	})
	return id
}
