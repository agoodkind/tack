package ops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

// ybTestDumpSpec uses production schema labels and events for endpoint tests.
func ybTestDumpSpec(outPath string) ybDumpSpec {
	return ybSchemaDumpSpec(&config.Config{YugabyteDB: "tack"}, outPath)
}

// TestYBDumpHosts checks single-node and quorum lists, bracketed IPv6,
// whitespace, duplicate entries, and empty entries. Every configured node
// remains a possible dump endpoint rather than depending only on the first.
func TestYBDumpHosts(t *testing.T) {
	tests := []struct {
		masters string
		want    []string
	}{
		{masters: "yugabyte:7100", want: []string{"yugabyte"}},
		{masters: "yb1:7100,yb2:7100,yb3:7100", want: []string{"yb1", "yb2", "yb3"}},
		{
			masters: "[3d06:bad:b01::10]:7100,yb2:7100",
			want:    []string{"3d06:bad:b01::10", "yb2"},
		},
		{masters: " yb1:7100 , yb2:7100 ,, yb1:7100 ", want: []string{"yb1", "yb2"}},
		{masters: "", want: nil},
	}
	for _, test := range tests {
		if got := ybDumpHosts(test.masters); !reflect.DeepEqual(got, test.want) {
			t.Errorf("ybDumpHosts(%q) = %v, want %v", test.masters, got, test.want)
		}
	}
}

// TestWalkYBDumpEndpointsPassesADownNode checks retry after an unavailable
// endpoint and termination at the first successful dump.
func TestWalkYBDumpEndpointsPassesADownNode(t *testing.T) {
	var tried []string
	dump := func(host string) (int64, string) {
		tried = append(tried, host)
		if host == "yb1" {
			return 0, "exited 2: could not connect to server: Connection refused"
		}

		return 4096, ""
	}

	err := walkYBDumpEndpoints(
		context.Background(),
		[]string{"yb1", "yb2", "yb3"},
		ybTestDumpSpec("/stage/schema.sql"),
		dump,
	)
	if err != nil {
		t.Fatalf("a serving endpoint after a down one must produce a dump: %v", err)
	}

	if !reflect.DeepEqual(tried, []string{"yb1", "yb2"}) {
		t.Fatalf("tried = %v, want termination after the first successful endpoint", tried)
	}
}

// TestWalkYBDumpEndpointsFailsWhenEveryEndpointRefuses checks a failure identifying
// every attempted endpoint and its refusal. The caller cannot publish a
// complete export without the required artifact.
func TestWalkYBDumpEndpointsFailsWhenEveryEndpointRefuses(t *testing.T) {
	var tried []string
	dump := func(host string) (int64, string) {
		tried = append(tried, host)
		return 0, "exited 2: could not connect to " + host
	}

	err := walkYBDumpEndpoints(
		context.Background(),
		[]string{"yb1", "yb2", "yb3"},
		ybTestDumpSpec("/stage/schema.sql"),
		dump,
	)
	if err == nil {
		t.Fatal("a dump must fail when every endpoint refuses it")
	}

	if !reflect.DeepEqual(tried, []string{"yb1", "yb2", "yb3"}) {
		t.Fatalf("tried = %v, want every configured endpoint", tried)
	}

	for _, host := range []string{"yb1", "yb2", "yb3"} {
		if !strings.Contains(err.Error(), host) {
			t.Errorf("the failure does not identify endpoint %s: %v", host, err)
		}

		if !strings.Contains(err.Error(), "could not connect to "+host) {
			t.Errorf("the failure does not report the refusal from %s: %v", host, err)
		}
	}

	if !strings.Contains(err.Error(), "schema") {
		t.Errorf("the failure does not identify the dump: %v", err)
	}
}

// TestWalkYBDumpEndpointsRejectsEmptyEndpointList checks rejection of an empty
// endpoint list instead of reporting an unattempted dump as successful.
func TestWalkYBDumpEndpointsRejectsEmptyEndpointList(t *testing.T) {
	dump := func(host string) (int64, string) {
		t.Fatalf("dump attempted against %q without a configured endpoint", host)
		return 0, ""
	}
	err := walkYBDumpEndpoints(context.Background(), nil, ybTestDumpSpec("/stage/schema.sql"), dump)
	if err == nil {
		t.Fatal("a dump must fail without a configured endpoint")
	}
}

// TestYBDumpAttemptOutcomeRefusesAnythingButAWrittenArtifact checks rejection
// of failed execution, unreadable output, and empty artifacts. Only a nonempty
// output file can restore the expected schema or roles.
func TestYBDumpAttemptOutcomeRefusesAnythingButAWrittenArtifact(t *testing.T) {
	dir := t.TempDir()
	written := filepath.Join(dir, "schema.sql")
	if err := os.WriteFile(written, []byte("CREATE TABLE t ();\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	empty := filepath.Join(dir, "empty.sql")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	size, reason := ybDumpAttemptOutcome(
		execResult{Stdout: "", Stderr: "", ExitCode: 0},
		nil,
		written,
	)
	if reason != "" || size != 19 {
		t.Fatalf("a written dump must succeed, got size=%d reason=%q", size, reason)
	}

	_, reason = ybDumpAttemptOutcome(execResult{Stdout: "", Stderr: "", ExitCode: 0}, nil, empty)
	if reason != "produced 0 bytes" {
		t.Fatalf("an empty dump must fail, got %q", reason)
	}

	_, reason = ybDumpAttemptOutcome(
		execResult{Stdout: "", Stderr: "", ExitCode: 0},
		nil,
		filepath.Join(dir, "never-written.sql"),
	)
	if !strings.Contains(reason, "stat ") {
		t.Fatalf("a missing dump file must fail, got %q", reason)
	}

	_, reason = ybDumpAttemptOutcome(
		execResult{
			Stdout:   "",
			Stderr:   "FATAL: password authentication failed",
			ExitCode: 2,
		},
		nil,
		written,
	)
	if !strings.Contains(reason, "exited 2") ||
		!strings.Contains(reason, "password authentication failed") {
		t.Fatalf("a failed dump must report its output, got %q", reason)
	}

	_, reason = ybDumpAttemptOutcome(
		execResult{Stdout: "", Stderr: "", ExitCode: 0},
		fmt.Errorf("create one-shot container: no such image"),
		written,
	)
	if !strings.Contains(reason, "no such image") {
		t.Fatalf("a container that did not run must report its failure, got %q", reason)
	}
}
