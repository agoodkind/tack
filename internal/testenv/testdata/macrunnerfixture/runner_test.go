//go:build macrunnerfixture && runtimeaudit

package integration

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMacRunnerFlags(t *testing.T) {
	t.Logf("MAC_RUNNER_FLAGS args=%q revision=%s temporary=%s", os.Args[1:], os.Getenv("TACK_TEST_SOURCE_REVISION"), os.TempDir())
	deadline, present := t.Deadline()
	if !present || time.Until(deadline) > 45*time.Second {
		t.Fatal("fixture requires the selected 45-second test timeout")
	}
}

func TestMacRunnerSkip(t *testing.T) {
	t.Fatal("the selected skip flag must exclude this fixture")
}

func TestMacRunnerFailure(t *testing.T) {
	t.Fatal("MAC_RUNNER_NONZERO")
}

func TestMacRunnerMutation(t *testing.T) {
	path := "example_test.go"
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(contents, []byte("\n// Fixture mutation.\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log("MAC_RUNNER_MUTATED")
}

func TestMacRunnerSignal(t *testing.T) {
	fmt.Println("MAC_RUNNER_SIGNAL_READY")
	time.Sleep(4 * time.Minute)
}

func TestMacRunnerCacheMarker(t *testing.T) {
	for _, path := range []string{"/root/.cache/go-build/mac-runner-marker", "/go/pkg/mod/mac-runner-marker"} {
		if err := os.WriteFile(path, []byte("MAC_RUNNER_CACHE_MARKER\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		contents, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(contents), "MAC_RUNNER_CACHE_MARKER") {
			t.Fatalf("cache marker: contents=%q error=%v", contents, err)
		}
	}
	t.Log("MAC_RUNNER_CACHE_MARKER")
}
