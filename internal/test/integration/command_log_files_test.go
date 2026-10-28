package integration_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCommandWritesItsOwnLogFiles runs the dry run of ops qa datagen search-mixed through the
// built server binary with LOG_COMMAND_DIR set. The command writes its records to one JSON
// file and one text file in that directory and does not create the shared LOG_JSON_FILE and
// LOG_TEXT_FILE files that the server rotates.
func TestCommandWritesItsOwnLogFiles(t *testing.T) {
	binary := buildMigrateServer(t)
	logs := t.TempDir()
	commandLogs := filepath.Join(logs, "commands")
	sharedJSON, sharedText := filepath.Join(logs, "app.jsonl"), filepath.Join(logs, "app.log")
	process := exec.CommandContext(t.Context(), binary,
		"--operator-service", "tack-command-log-test", "ops", "qa", "datagen", "search-mixed")
	process.Env = append(os.Environ(), "DATABASE_URL=postgres://command-log-test.invalid/tack",
		"LOG_DISABLE_STDOUT=true", "LOG_JSON_FILE="+sharedJSON, "LOG_TEXT_FILE="+sharedText,
		"LOG_COMMAND_DIR="+commandLogs)
	if output, err := process.CombinedOutput(); err != nil {
		t.Fatalf("the search-mixed dry run failed: %v\n%s", err, output)
	}
	for _, pattern := range []string{"ops-qa-datagen-search-mixed-*.jsonl", "ops-qa-datagen-search-mixed-*.log"} {
		matches, err := filepath.Glob(filepath.Join(commandLogs, pattern))
		if err != nil || len(matches) != 1 {
			t.Fatalf("the command log directory contains %v for %s, want one file (error %v)", matches, pattern, err)
		}
		contents, err := os.ReadFile(matches[0])
		if err != nil || !bytes.Contains(contents, []byte("cli.start")) {
			t.Fatalf("the command log file %s does not contain the cli.start record (read error %v)", matches[0], err)
		}
	}
	for _, shared := range []string{sharedJSON, sharedText} {
		if _, err := os.Stat(shared); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the command created the shared log file %s (stat error %v)", shared, err)
		}
	}
}
