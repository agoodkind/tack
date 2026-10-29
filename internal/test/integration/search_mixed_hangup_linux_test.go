package integration_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

const (
	hangupMixedDuration = 40 * time.Second
	hangupMixedCatchUp  = 5 * time.Minute
	// hangupExitMargin allows time beyond the measured window and catch-up
	// for setup after hangup, later workload phases, and writing the result.
	hangupExitMargin = 3 * time.Minute
	// hangupSetupLimit limits the wait for the burst record, including corpus
	// creation and the wait for search to return every corpus issue.
	hangupSetupLimit       = 10 * time.Minute
	hangupBurstRecord      = "qa.datagen.search_mixed_burst_finished"
	hangupIgnoredRecord    = "cli.hangup_ignored"
	hangupMixedResult      = "qa.datagen.search_mixed_result"
	hangupMixedStopReason  = "none"
	hangupOperatorEmail    = "hangup@example.test"
	hangupCommandLogFolder = "commands"
)

// TestSearchMixedKeepsRunningAfterHangup checks that search-mixed completes
// after its controlling terminal closes during the measured window.
// It requires cli.hangup_ignored as proof that the process received SIGHUP,
// a successful exit, a result with stop_reason "none", and a completed
// audit operation.
func TestSearchMixedKeepsRunningAfterHangup(t *testing.T) {
	cfg := newCohortEnginesOn(t, testenv.FreshFoundationDB(t), nil)
	t.Setenv("OPENSEARCH_WORKER_CONCURRENCY", "1")
	t.Setenv("OPENSEARCH_WORKER_IDLE_INTERVAL", "1s")
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", "30s")
	endpoints := startSearchMixedProcesses(t, cfg)
	logs := t.TempDir()
	commandLogs := filepath.Join(logs, hangupCommandLogFolder)
	environment := append(os.Environ(), "TACK_DATAGEN_ALLOW_TARGET=local",
		"AUDIT_OPERATOR_DSN="+cfg.DatabaseURL, "AUDIT_WRITER_DSN="+cfg.DatabaseURL, "AUDIT_KAFKA_BROKERS=",
		"AUDIT_ALLOW_UNRECORDED=false", "LOG_DISABLE_STDOUT=true", "LOG_JSON_FILE="+filepath.Join(logs, "app.jsonl"),
		"LOG_TEXT_FILE="+filepath.Join(logs, "app.log"), "LOG_COMMAND_DIR="+commandLogs)
	operatorID := uuid.Must(uuid.NewV7())
	process := startInTerminal(t, buildMigrateServer(t), environment,
		"--execute", "--output", "json", "--operator-id", operatorID.String(), "--operator-email", hangupOperatorEmail,
		"--operator-name", "Hangup Test", "ops", "qa", "datagen", "search-mixed",
		"--seed", strconv.FormatInt(clock.Now().UnixNano(), 10), "--rate", "60", "--write-rate", "60",
		"--duration", hangupMixedDuration.String(), "--concurrency", "4", "--burst-writes", "20",
		"--catch-up", hangupMixedCatchUp.String(), "--endpoints", strings.Join(endpoints, ","), "--commit")
	commandLog := filepath.Join(commandLogs, commandLogPrefix+"*.jsonl")
	process.waitForLogRecord(t, commandLog, hangupBurstRecord, hangupSetupLimit)
	process.hangUp(t)
	status := process.waitForExit(t, hangupMixedDuration+hangupMixedCatchUp+hangupExitMargin)
	if status.Signaled() {
		t.Fatalf("search-mixed ended by signal %s after the terminal hangup:\n%s", status.Signal(), process.output)
	}
	if status.ExitStatus() != 0 {
		t.Fatalf("search-mixed exited with status %d after the terminal hangup:\n%s", status.ExitStatus(), process.output)
	}
	if !logContainsRecord(t, commandLog, hangupIgnoredRecord) {
		t.Fatalf("the command log %s does not contain %s. The process did not receive SIGHUP", commandLog, hangupIgnoredRecord)
	}
	if stopReason := searchMixedLoggedStopReason(t, commandLog); stopReason != hangupMixedStopReason {
		t.Fatalf("the %s record has stop_reason %q, want %q", hangupMixedResult, stopReason, hangupMixedStopReason)
	}
	events := outboxEventsByQuery(t, ledgerAdminPool(t), "runtime_audit_verb_outbox.sql",
		string(audit.VerbOpsDatagenSoak), operatorID.String())
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	if len(intents) != 1 {
		t.Fatalf("the outbox contains %d search-mixed intents for operator %s, want 1", len(intents), operatorID)
	}
	requireCompletedResult(t, events, intents[0].Context.OperationID)
}

// searchMixedLoggedStopReason returns stop_reason from the first
// qa.datagen.search_mixed_result record in the single matching command log.
// It fails the test if the log does not contain that record.
func searchMixedLoggedStopReason(t *testing.T, pattern string) string {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) != 1 {
		t.Fatalf("the command log directory contains %v for %s, want one file (error %v)", matches, pattern, err)
	}
	file, err := os.Open(matches[0])
	if err != nil {
		t.Fatalf("open the command log %s: %v", matches[0], err)
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<16), 1<<24)
	for scanner.Scan() {
		var record struct {
			Message    string `json:"msg"`
			StopReason string `json:"stop_reason"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) == nil && record.Message == hangupMixedResult {
			return record.StopReason
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read the command log %s: %v", matches[0], err)
	}
	t.Fatalf("the command log %s does not contain the %s record", matches[0], hangupMixedResult)
	return ""
}
