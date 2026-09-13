package telemetry

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every record the configured logger writes carries the environment it was
// configured with, not only the startup line (TACK-263).
func TestSetupStampsEnvOnEveryRecord(t *testing.T) {
	jsonFile := filepath.Join(t.TempDir(), "app.jsonl")
	closer, err := Setup(LogConfig{
		Level:         "info",
		JSONFile:      jsonFile,
		DisableStdout: true,
		Env:           "production",
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	slog.Info("test.first")
	slog.Info("test.second", slog.String("k", "v"))
	if err := closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(jsonFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %s", len(lines), raw)
	}
	for _, line := range lines {
		if !strings.Contains(line, `"env":"production"`) {
			t.Fatalf("line lacks env attribute: %s", line)
		}
		if !strings.Contains(line, `"build":`) {
			t.Fatalf("line lacks build attribute: %s", line)
		}
	}
}
