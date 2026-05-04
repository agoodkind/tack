package main

import (
	"io"
	"os"
	"strings"
	"testing"

	repaircli "goodkind.io/tack/internal/repair"
)

func TestRunRepairPrintsUsageForHelp(t *testing.T) {
	stderr := captureRepairStderr(t, func() {
		repaircli.Run(nil, []string{"help"})
	})
	if !strings.Contains(stderr, "usage: ./server repair <command> [flags]") {
		t.Fatalf("stderr = %q want usage text", stderr)
	}
	if !strings.Contains(stderr, "preview    Show the exact repair plan and confirmation token") {
		t.Fatalf("stderr = %q want preview command", stderr)
	}
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
