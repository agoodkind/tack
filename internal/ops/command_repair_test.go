package ops

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRunRepairPrintsUsageForHelp(t *testing.T) {
	stderr := captureRepairStderr(t, func() {
		RunCommand(context.Background(), nil, []string{"repair", "help"})
	})
	if !strings.Contains(stderr, "usage: ./server ops repair <classes|preview|apply> [flags]") {
		t.Fatalf("stderr = %q want usage text", stderr)
	}
	if !strings.Contains(stderr, "Preview a targeted repair for one node") {
		t.Fatalf("stderr = %q want preview command", stderr)
	}
	if !strings.Contains(stderr, "Apply a previously previewed repair after explicit confirmation") {
		t.Fatalf("stderr = %q want apply command", stderr)
	}
}

func TestRepairCLIHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_REPAIR_HELPER_PROCESS") != "1" {
		return
	}

	switch os.Getenv("REPAIR_HELPER_SCENARIO") {
	case "run-unknown":
		RunCommand(context.Background(), nil, []string{"repair", "unknown"})
	case "parse-invalid-node":
		RunCommand(context.Background(), nil, []string{"repair", "preview", "--node", "not-a-uuid"})
	case "apply-missing-confirm":
		RunCommand(context.Background(), nil, []string{"repair", "apply", "--node", uuid.New().String(), "--actor", uuid.New().String(), "--confirm", "   "})
	default:
		t.Fatalf("unknown helper scenario %q", os.Getenv("REPAIR_HELPER_SCENARIO"))
	}
}

func TestRunRepairUnknownCommandExits(t *testing.T) {
	result := runRepairHelper(t, "run-unknown")
	if result.exitCode != 2 {
		t.Fatalf("exitCode = %d want 2", result.exitCode)
	}
	if !strings.Contains(result.stderr, "unknown repair command: unknown") {
		t.Fatalf("stderr = %q want unknown-command error", result.stderr)
	}
}

func TestRunRepairPreviewRejectsInvalidUUID(t *testing.T) {
	result := runRepairHelper(t, "parse-invalid-node")
	if result.exitCode != 2 {
		t.Fatalf("exitCode = %d want 2", result.exitCode)
	}
	if !strings.Contains(result.stderr, "repair preview: --node must be a UUID") {
		t.Fatalf("stderr = %q want invalid-uuid error", result.stderr)
	}
}

func TestRunRepairApplyRejectsBlankConfirmationToken(t *testing.T) {
	result := runRepairHelper(t, "apply-missing-confirm")
	if result.exitCode != 2 {
		t.Fatalf("exitCode = %d want 2", result.exitCode)
	}
	if !strings.Contains(result.stderr, "usage: ./server ops repair apply --node <uuid> --actor <uuid> --confirm <token>") {
		t.Fatalf("stderr = %q want apply usage", result.stderr)
	}
}

type repairHelperResult struct {
	exitCode int
	stderr   string
}

func runRepairHelper(t *testing.T, scenario string) repairHelperResult {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=TestRepairCLIHelperProcess")
	command.Env = append(os.Environ(), "GO_WANT_REPAIR_HELPER_PROCESS=1", "REPAIR_HELPER_SCENARIO="+scenario)
	output, err := command.CombinedOutput()
	exitCode := 0
	if err != nil {
		exitError, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("helper command err = %v", err)
		}
		exitCode = exitError.ExitCode()
	}
	return repairHelperResult{exitCode: exitCode, stderr: string(output)}
}

func captureRepairStderr(t *testing.T, run func()) string {
	t.Helper()
	originalStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = writer
	run()
	_ = writer.Close()
	os.Stderr = originalStderr
	output, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil {
		t.Fatalf("ReadAll stderr pipe: %v", readErr)
	}
	return string(output)
}
