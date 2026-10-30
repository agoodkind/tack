package integration

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestThroughputWorkerFailureDiagnosticsChild(t *testing.T) {
	mode := os.Getenv("TACK_DIAGNOSTIC_CHILD_CASE")
	if mode == "" {
		t.Skip("requires the diagnostic regression subprocess")
	}
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	secret := "synthetic-diagnostic-secret"
	if mode == "expanded" {
		secret = "q"
	}
	server := &actualSearchServer{command: &exec.Cmd{Process: process, Env: []string{"DIAGNOSTIC_SECRET=" + secret}}, logPath: os.Getenv("TACK_DIAGNOSTIC_CHILD_LOG")}
	corpus := processThroughputCorpus{workerIDs: []uuid.UUID{uuid.MustParse("018b6437-0000-7000-8000-000000000001")}}
	requireThroughputWorkerEvidence(t, corpus, []*actualSearchServer{server}, []int64{70000})
}

const (
	throughputLogScanBytes   = 1048576
	throughputContextBytes   = 1024
	throughputContextEntries = 3
)

func recordActualServerFailure(t *testing.T, path string, environment []string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Errorf("read actual server failure evidence: %v", err)
		return
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 65536))
	if err != nil {
		t.Errorf("read bounded actual server failure evidence: %v", err)
		return
	}
	text := redactActualDiagnostic(string(body), environment)
	for line := range strings.SplitSeq(text, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "bearer ") || strings.Contains(lower, "authorization") {
			continue
		}
		if line != "" {
			t.Logf("actual server failure evidence: %s", line)
		}
	}
}

func redactActualDiagnostic(text string, environment []string) string {
	for _, variable := range environment {
		key, value, _ := strings.Cut(variable, "=")
		key = strings.ToUpper(key)
		if value != "" && (strings.Contains(key, "PASSWORD") || strings.Contains(key, "TOKEN") || strings.Contains(key, "SECRET") || strings.Contains(key, "KEY") || strings.Contains(key, "DSN") || key == "DATABASE_URL") {
			text = strings.ReplaceAll(text, value, "[redacted]")
		}
	}
	credentials := regexp.MustCompile(`(?i)(postgres(?:ql)?|https?)://[^@\s]+@`)
	text = credentials.ReplaceAllString(text, "${1}://[redacted]@")
	return text
}

func sanitizeActualProtocolMessage(message string, environment []string, token string) string {
	text := message
	if token != "" {
		text = strings.ReplaceAll(text, token, "[redacted]")
	}
	text = redactActualDiagnostic(text, environment)
	if strings.Contains(strings.ToLower(text), "bearer ") || strings.Contains(strings.ToLower(text), "authorization") {
		return "[redacted protocol message]"
	}
	if len(text) > 1024 {
		return text[:1024]
	}
	return text
}

func boundedThroughputDiagnostic(text string, environment []string, limit int) string {
	text = redactActualDiagnostic(text, environment)
	lower := strings.ToLower(text)
	if strings.Contains(lower, "bearer ") || strings.Contains(lower, "authorization") {
		return "[suppressed sensitive entry]"
	}
	const truncated = "[truncated]"
	if len(text) > limit {
		return text[:limit-len(truncated)] + truncated
	}
	return text
}

func requireThroughputWorkerEvidence(t *testing.T, corpus processThroughputCorpus, servers []*actualSearchServer, offsets []int64) {
	t.Helper()
	nodes := make(map[string]bool, len(corpus.workerIDs))
	for _, id := range corpus.workerIDs {
		nodes[id.String()] = true
	}
	for ordinal, server := range servers {
		file, err := os.Open(server.logPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Seek(offsets[ordinal], 0); err != nil {
			file.Close()
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), throughputLogScanBytes)
		claims := 0
		recent := make([]string, 0, throughputContextEntries)
		for scanner.Scan() {
			entry := scanner.Text()
			var event struct {
				Message string `json:"msg"`
				Class   string `json:"class"`
				NodeID  string `json:"node_id"`
				Pages   int    `json:"pages"`
				Level   string `json:"level"`
			}
			parsed := json.Unmarshal(scanner.Bytes(), &event) == nil
			if parsed && event.Level == "ERROR" && (nodes[event.NodeID] || strings.HasPrefix(event.Message, "search.")) {
				t.Logf("throughput worker failure pid=%d log=%s scan_start=%d", server.command.Process.Pid, server.logPath, offsets[ordinal])
				for _, preceding := range recent {
					t.Logf("throughput preceding entry: %s", preceding)
				}
				t.Logf("throughput offending event: %s", boundedThroughputDiagnostic(entry, server.command.Env, throughputLogScanBytes))
				file.Close()
				t.Fatalf("production worker pid=%d logged an error for a measured node", server.command.Process.Pid)
			}
			if len(recent) == throughputContextEntries {
				recent = recent[1:]
			}
			recent = append(recent, boundedThroughputDiagnostic(entry, server.command.Env, throughputContextBytes))
			if parsed && event.Message == "search.worker.slice_completed" && event.Class == "live" && nodes[event.NodeID] && event.Pages > 0 {
				claims++
			}
		}
		scanError := scanner.Err()
		closeError := file.Close()
		if scanError != nil || closeError != nil || claims == 0 {
			t.Fatalf("production worker pid=%d has no verified measured live slice: scan=%v close=%v", server.command.Process.Pid, scanError, closeError)
		}
		t.Logf("throughput production worker pid=%d measured_live_slices=%d", server.command.Process.Pid, claims)
	}
}
