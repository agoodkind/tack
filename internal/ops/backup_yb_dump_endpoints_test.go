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

// ybTestDumpSpec returns the production schema-dump specification. Endpoint
// tests use its actual label and event names.
func ybTestDumpSpec(outPath string) ybDumpSpec {
	return ybSchemaDumpSpec(&config.Config{YugabyteDB: "tack"}, outPath)
}

// TestYBDumpHosts checks the single-node default, a quorum list, a bracketed
// IPv6 literal for the IPv6-only bridge, and whitespace, duplicate, and empty
// entries. All listed nodes can supply dump endpoints. The earlier
// first-entry selection prevented schema and role dumps when that node
// stopped even if the remaining quorum served requests.
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

// TestWalkYBDumpEndpointsPassesADownNode uses a callback that refuses yb1
// and succeeds on yb2. It checks that the function stops before trying yb3
// after that success.
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
		t.Fatalf("tried = %v, want the walk to stop at the first endpoint that served", tried)
	}
}

// TestWalkYBDumpEndpointsFailsLoudlyWhenNoneServe checks that the walker
// returns one error when every endpoint refuses. The error includes the dump
// label and each attempted host and reason. The caller must not upload a run
// without a required restore file.
func TestWalkYBDumpEndpointsFailsLoudlyWhenNoneServe(t *testing.T) {
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
		t.Fatal("a dump no endpoint served must fail")
	}

	if !reflect.DeepEqual(tried, []string{"yb1", "yb2", "yb3"}) {
		t.Fatalf("tried = %v, want every configured endpoint", tried)
	}

	for _, host := range []string{"yb1", "yb2", "yb3"} {
		if !strings.Contains(err.Error(), host) {
			t.Errorf("the failure does not name endpoint %s: %v", host, err)
		}

		if !strings.Contains(err.Error(), "could not connect to "+host) {
			t.Errorf("the failure does not say what %s did: %v", host, err)
		}
	}

	if !strings.Contains(err.Error(), "schema") {
		t.Errorf("the failure does not say which dump it was: %v", err)
	}
}

// TestWalkYBDumpEndpointsWithNoConfiguredEndpoint checks that an empty
// endpoint list fails without invoking the dump callback.
func TestWalkYBDumpEndpointsWithNoConfiguredEndpoint(t *testing.T) {
	dump := func(host string) (int64, string) {
		t.Fatalf("dump attempted against %q with no configured endpoint", host)
		return 0, ""
	}
	err := walkYBDumpEndpoints(context.Background(), nil, ybTestDumpSpec("/stage/schema.sql"), dump)
	if err == nil {
		t.Fatal("a dump with no endpoint to run against must fail")
	}
}

// TestYBDumpAttemptOutcomeRefusesAnythingButAWrittenArtifact checks which
// results qualify as a dump. Container errors, nonzero exits, missing files,
// and zero-byte files must fail. A successful exit requires a nonempty
// artifact for restore, even when the process reports success.
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
		t.Fatalf("a written dump must be accepted, got size=%d reason=%q", size, reason)
	}

	_, reason = ybDumpAttemptOutcome(execResult{Stdout: "", Stderr: "", ExitCode: 0}, nil, empty)
	if reason != "produced 0 bytes" {
		t.Fatalf("an empty dump must be refused, got %q", reason)
	}

	_, reason = ybDumpAttemptOutcome(
		execResult{Stdout: "", Stderr: "", ExitCode: 0},
		nil,
		filepath.Join(dir, "never-written.sql"),
	)
	if !strings.Contains(reason, "stat ") {
		t.Fatalf("a missing dump file must be refused, got %q", reason)
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
		t.Fatalf("a failed dump must be refused with what it printed, got %q", reason)
	}

	_, reason = ybDumpAttemptOutcome(
		execResult{Stdout: "", Stderr: "", ExitCode: 0},
		fmt.Errorf("create one-shot container: no such image"),
		written,
	)
	if !strings.Contains(reason, "no such image") {
		t.Fatalf("a one-shot that never ran must be refused with the reason, got %q", reason)
	}
}
