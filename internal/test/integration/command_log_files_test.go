package integration_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
)

const (
	commandLogPrefix  = "ops-qa-datagen-search-mixed-"
	expiredCommandLog = "ops-qa-datagen-search-mixed-0190a000-0000-7000-8000-000000000001.jsonl"
	recentCommandLog  = "ops-qa-datagen-search-mixed-0190a000-0000-7000-8000-000000000002.log"
	expiredOtherLog   = "operator-notes.log"
)

// TestCommandWritesItsOwnLogFiles runs the dry run of ops qa datagen search-mixed through the
// built server binary with LOG_COMMAND_DIR set to a directory that does not exist. The
// command creates the directory, writes its records to one JSON file and one text file in it,
// and does not create the shared LOG_JSON_FILE and LOG_TEXT_FILE files that the server
// rotates. A second run with LOG_MAX_AGE_DAYS=1 deletes the command log file modified two days
// ago and keeps the recent command log file and the old file with another name.
func TestCommandWritesItsOwnLogFiles(t *testing.T) {
	binary := buildMigrateServer(t)
	logs := t.TempDir()
	commandLogs := filepath.Join(logs, "commands")
	sharedJSON, sharedText := filepath.Join(logs, "app.jsonl"), filepath.Join(logs, "app.log")
	environment := append(os.Environ(), "DATABASE_URL=postgres://command-log-test.invalid/tack",
		"LOG_DISABLE_STDOUT=true", "LOG_JSON_FILE="+sharedJSON, "LOG_TEXT_FILE="+sharedText,
		"LOG_COMMAND_DIR="+commandLogs, "LOG_MAX_AGE_DAYS=1")
	runSearchMixedDryRun(t, binary, environment)
	for _, pattern := range []string{commandLogPrefix + "*.jsonl", commandLogPrefix + "*.log"} {
		requireOneCommandLog(t, filepath.Join(commandLogs, pattern), "cli.start")
	}
	for _, shared := range []string{sharedJSON, sharedText} {
		if _, err := os.Stat(shared); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the command created the shared log file %s (stat error %v)", shared, err)
		}
	}

	twoDaysAgo := clock.Now().Add(-48 * time.Hour)
	writeCommandLogFixture(t, filepath.Join(commandLogs, expiredCommandLog), twoDaysAgo)
	writeCommandLogFixture(t, filepath.Join(commandLogs, recentCommandLog), clock.Now())
	writeCommandLogFixture(t, filepath.Join(commandLogs, expiredOtherLog), twoDaysAgo)
	runSearchMixedDryRun(t, binary, environment)
	if _, err := os.Stat(filepath.Join(commandLogs, expiredCommandLog)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the second run kept the expired command log file %s (stat error %v)", expiredCommandLog, err)
	}
	for _, kept := range []string{recentCommandLog, expiredOtherLog} {
		if _, err := os.Stat(filepath.Join(commandLogs, kept)); err != nil {
			t.Fatalf("the second run deleted %s: %v", kept, err)
		}
	}
	matches, err := filepath.Glob(filepath.Join(commandLogs, commandLogPrefix+"*.jsonl"))
	if err != nil || len(matches) != 2 {
		t.Fatalf("the command log directory contains %v JSON files, want the two runs' files (error %v)", matches, err)
	}
	removedRecord := []byte(`"msg":"log.command_files_removed"`)
	found := false
	for _, match := range matches {
		contents, readErr := os.ReadFile(match)
		if readErr == nil && bytes.Contains(contents, removedRecord) && bytes.Contains(contents, []byte(`"removed":1`)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no command log file records log.command_files_removed with removed 1: %v", matches)
	}
	requireUnlistableCommandLogsStopCommand(t, binary, logs)
}

// requireUnlistableCommandLogsStopCommand runs the dry run with LOG_COMMAND_DIR set to a
// directory with mode 0300, which the process can write to but cannot list. The command must
// exit with a nonzero status before it prints the dry run plan, and its JSON log file must
// record log.command_files_list_failed. The check skips when the test runs as root, because
// root lists the directory despite its mode.
func requireUnlistableCommandLogsStopCommand(t *testing.T, binary, logs string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Log("skipping the unlistable LOG_COMMAND_DIR check because root can list a directory with mode 0300")
		return
	}
	unlistable := filepath.Join(logs, "unlistable")
	if err := os.Mkdir(unlistable, 0o300); err != nil {
		t.Fatalf("create the unlistable command log directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unlistable, 0o700) })
	process := exec.CommandContext(t.Context(), binary,
		"--operator-service", "tack-command-log-test", "ops", "qa", "datagen", "search-mixed")
	process.Env = append(os.Environ(), "DATABASE_URL=postgres://command-log-test.invalid/tack",
		"LOG_DISABLE_STDOUT=true", "LOG_JSON_FILE="+filepath.Join(logs, "app.jsonl"),
		"LOG_TEXT_FILE="+filepath.Join(logs, "app.log"), "LOG_COMMAND_DIR="+unlistable, "LOG_MAX_AGE_DAYS=1")
	output, err := process.CombinedOutput()
	if err == nil || bytes.Contains(output, []byte("would run")) {
		t.Fatalf("the dry run with an unlistable LOG_COMMAND_DIR = %v, want a nonzero exit before the plan:\n%s", err, output)
	}
	if err := os.Chmod(unlistable, 0o700); err != nil {
		t.Fatalf("make the command log directory listable: %v", err)
	}
	requireOneCommandLog(t, filepath.Join(unlistable, commandLogPrefix+"*.jsonl"), "log.command_files_list_failed")
}

func runSearchMixedDryRun(t *testing.T, binary string, environment []string) {
	t.Helper()
	process := exec.CommandContext(t.Context(), binary,
		"--operator-service", "tack-command-log-test", "ops", "qa", "datagen", "search-mixed")
	process.Env = environment
	if output, err := process.CombinedOutput(); err != nil {
		t.Fatalf("the search-mixed dry run failed: %v\n%s", err, output)
	}
}

func requireOneCommandLog(t *testing.T, pattern, record string) {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) != 1 {
		t.Fatalf("the command log directory contains %v for %s, want one file (error %v)", matches, pattern, err)
	}
	contents, err := os.ReadFile(matches[0])
	if err != nil || !bytes.Contains(contents, []byte(record)) {
		t.Fatalf("the command log file %s does not contain the %s record (read error %v)", matches[0], record, err)
	}
}

func writeCommandLogFixture(t *testing.T, path string, modified time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte("fixture\n"), 0o600); err != nil {
		t.Fatalf("write the log file fixture %s: %v", path, err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatalf("set the modification time of %s: %v", path, err)
	}
}
