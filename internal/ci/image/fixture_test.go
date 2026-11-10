package image_test

import (
	"os"
	"testing"
)

const (
	linuxPlatform = "linux"
	testFileMode  = 0o600
	testDirMode   = 0o750
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()

	err := os.WriteFile(path, []byte(content), testFileMode)
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
