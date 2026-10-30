package testenv_test

import (
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerPortableValidation(t *testing.T) {
	options := testenv.MacTestOptions{
		Root: t.TempDir(), EvidenceDirectory: filepath.Join(t.TempDir(), "evidence"),
		Image:         "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		TestArguments: []string{"-run=TestFixture", "-count=1", "-timeout=1m"},
	}
	if err := options.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"-tags=", "-skip="} {
		t.Run(flag, func(t *testing.T) {
			invalid := options
			invalid.TestArguments = append([]string{"-run=TestFixture", "-count=1", "-timeout=1m"}, flag)
			if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "nonempty value") {
				t.Fatalf("reject empty optional input: %v", err)
			}
		})
	}
}
