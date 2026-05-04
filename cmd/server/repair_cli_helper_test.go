package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/uuid"
	repaircli "goodkind.io/tack/internal/repair"
)

func TestRepairCLIHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_REPAIR_HELPER_PROCESS") != "1" {
		return
	}

	scenario := os.Getenv("REPAIR_HELPER_SCENARIO")
	switch scenario {
	case "run-unknown":
		repaircli.Run(nil, []string{"unknown"})
	case "parse-invalid-node":
		repaircli.Run(nil, []string{"preview", "--node", "not-a-uuid"})
	case "apply-missing-confirm":
		repaircli.Run(nil, []string{"apply", "--node", uuid.New().String(), "--actor", uuid.New().String(), "--confirm", "   "})
	default:
		t.Fatalf("unknown helper scenario %q", scenario)
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
	if !strings.Contains(result.stderr, "preview: --node must be a UUID") {
		t.Fatalf("stderr = %q want invalid-uuid error", result.stderr)
	}
}

func TestRunRepairApplyRejectsBlankConfirmationToken(t *testing.T) {
	result := runRepairHelper(t, "apply-missing-confirm")
	if result.exitCode != 2 {
		t.Fatalf("exitCode = %d want 2", result.exitCode)
	}
	if !strings.Contains(result.stderr, "usage: ./server repair apply --node <uuid> --actor <uuid> --confirm <token>") {
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
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			t.Fatalf("helper command err = %v", err)
		}
	}
	return repairHelperResult{exitCode: exitCode, stderr: string(output)}
}
