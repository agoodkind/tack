package integration_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	diagnosticChildCase    = "TACK_DIAGNOSTIC_CHILD_CASE"
	diagnosticChildLog     = "TACK_DIAGNOSTIC_CHILD_LOG"
	diagnosticNode         = "018b6437-0000-7000-8000-000000000001"
	diagnosticSecret       = "synthetic-diagnostic-secret"
	diagnosticPrefixBytes  = 70000
	diagnosticSelector     = "^TestThroughputWorkerFailureDiagnosticsChild$"
	throughputContextBytes = 1024
	throughputLogScanBytes = 1048576
)

func TestThroughputWorkerFailureDiagnostics(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"measured", "search", "suppressed", "expanded", "passing", "missing_claim"} {
		t.Run(mode, func(t *testing.T) {
			message, nodeID := "worker.failed", diagnosticNode
			if mode == "search" {
				message, nodeID = "search.failed", "unmeasured"
			}
			errorText := "diagnostic failure " + diagnosticSecret + " https://synthetic-user:synthetic-password@example.test/path " + strings.Repeat("z", 2000) + " event-tail"
			if mode == "suppressed" {
				errorText = "Authorization: Bearer synthetic-bearer"
			}
			if mode == "expanded" {
				errorText = strings.Repeat("q", 120000)
			}
			event := fmt.Sprintf(`{"level":"ERROR","msg":%q,"node_id":%q,"error":%q}`, message, nodeID, errorText)
			claim := fmt.Sprintf(`{"msg":"search.worker.slice_completed","class":"live","node_id":%q,"pages":1}`, diagnosticNode)
			if mode == "passing" || mode == "missing_claim" {
				event = `{"level":"ERROR","msg":"unrelated.failed","node_id":"unmeasured"}`
			}
			if mode == "missing_claim" {
				claim = fmt.Sprintf(`{"msg":"search.worker.slice_completed","class":"rebuild","node_id":%q,"pages":1}`, diagnosticNode)
			}
			prefix := strings.Repeat("p", diagnosticPrefixBytes-1) + "\n"
			contextSecret := diagnosticSecret
			if mode == "expanded" {
				contextSecret = "q"
			}
			context := "older-unrelated\ncurrent-context " + contextSecret + " https://synthetic-user:synthetic-password@example.test/path " + strings.Repeat("x", 2000) + " context-tail\nAuthorization: Bearer synthetic-context-token\nlatest-context\n"
			path := filepath.Join(t.TempDir(), "server.log")
			if err := os.WriteFile(path, []byte(prefix+claim+"\n"+context+event+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), executable, "-test.run="+diagnosticSelector, "-test.count=1", "-test.timeout=15s", "-test.v")
			command.Env = append(os.Environ(), diagnosticChildCase+"="+mode, diagnosticChildLog+"="+path)
			output, runError := command.CombinedOutput()
			if mode == "passing" {
				if runError != nil || !strings.Contains(string(output), "measured_live_slices=1") {
					t.Fatal("live-slice control did not verify exactly one measured claim")
				}
				return
			}
			var exitError *exec.ExitError
			if !errors.As(runError, &exitError) || exitError.ExitCode() != 1 {
				t.Fatalf("diagnostic subprocess did not fail its assertion: %v", runError)
			}
			text := string(output)
			if mode == "missing_claim" {
				if !strings.Contains(text, "has no verified measured live slice") {
					t.Fatal("non-live claim did not fail the existing assertion")
				}
				return
			}
			for _, required := range []string{"pid=" + strconv.Itoa(command.Process.Pid), "log=" + path, "scan_start=70000", "current-context [redacted] https://[redacted]@example.test/path", "[truncated]", "[suppressed sensitive entry]", "latest-context", "logged an error for a measured node"} {
				if !strings.Contains(text, required) {
					t.Fatalf("diagnostic output omitted required evidence %q", required)
				}
			}
			for _, forbidden := range []string{diagnosticSecret, "synthetic-user", "synthetic-password", "synthetic-bearer", "synthetic-context-token", "older-unrelated", "context-tail", strings.Repeat("p", 100)} {
				if strings.Contains(text, forbidden) {
					t.Fatal("diagnostic output included a secret or unrelated context")
				}
			}
			if mode == "suppressed" {
				if !strings.Contains(text, "offending event: [suppressed sensitive entry]") {
					t.Fatal("sensitive offending event did not produce an explicit suppression marker")
				}
			} else if !strings.Contains(text, `"msg":"`+message+`"`) || !strings.Contains(text, `"node_id":"`+nodeID+`"`) || !strings.Contains(text, `"error":"`) {
				t.Fatal("offending event omitted message, node, or error fields")
			}
			if mode == "measured" || mode == "search" {
				if !strings.Contains(text, "event-tail") {
					t.Fatal("offending event used the protocol message truncation limit")
				}
			}
			for line := range strings.SplitSeq(text, "\n") {
				if _, entry, found := strings.Cut(line, "throughput preceding entry: "); found && len(entry) > throughputContextBytes {
					t.Fatal("preceding entry exceeded the diagnostic bound")
				}
				if _, entry, found := strings.Cut(line, "throughput offending event: "); found && len(entry) > throughputLogScanBytes {
					t.Fatal("offending event exceeded the diagnostic bound")
				}
			}
			t.Logf("real diagnostic subprocess pid=%d case=%s exit=1 verified bounded redacted evidence", command.Process.Pid, mode)
		})
	}
}
