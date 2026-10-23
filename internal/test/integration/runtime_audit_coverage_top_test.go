//go:build runtimeaudit

package integration_test

import (
	"bufio"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// topLevelCase pairs a command runner with the audit verb and outcome that the
// operator outbox must record.
type topLevelCase struct {
	run     func(t *testing.T, env *topLevelEnv) uuid.UUID
	verb    audit.Verb
	outcome audit.Outcome
}

// serveStartedLog is the log text that runServe counts to detect server
// startup.
const serveStartedLog = "starting server"

// topLevelHiddenAliases lists hidden command aliases that the coverage test
// checks separately from command completion.
var topLevelHiddenAliases = []string{"audit-export", "audit-verify", "gen-audit-key"}

// TestRuntimeAuditTopLevelCoverage discovers commands through the server
// binary's completion command and checks hidden aliases separately. It requires
// each command to have an executable audit case or a static help check.
func TestRuntimeAuditTopLevelCoverage(t *testing.T) {
	env := startTopLevelEnv(t)
	operations := env.operations(t)
	cases := topLevelCases()
	excluded := map[string]string{"command tack audit": staticHelpReason}
	var missing []string
	for _, key := range operations {
		_, hasCase := cases[key]
		_, isExcluded := excluded[key]
		if !hasCase && !isExcluded {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		t.Errorf("Discovered commands do not have coverage entries; each command requires an executable case or an exclusion:\n%s", strings.Join(missing, "\n"))
	}
	for _, key := range slices.Sorted(maps.Keys(cases)) {
		if !slices.Contains(operations, key) {
			t.Errorf("Command discovery did not include the command; the test requires it for coverage case %q", key)
		}
	}
	for _, key := range operations {
		if _, isExcluded := excluded[key]; isExcluded {
			t.Run(key, func(t *testing.T) { env.requireStaticHelp(t, key) })
			continue
		}
		if current, found := cases[key]; found {
			t.Run(key, func(t *testing.T) {
				operatorID := current.run(t, env)
				requireOutboxEvent(t, coverageEvidence{
					orgID: audit.SystemOrgID(), requestID: "", actorID: operatorID, outcome: "", outbox: env.admin,
				}, current.verb, current.outcome)
			})
		}
	}
}

// operations recursively discovers commands through completion and checks
// hidden aliases with --help. It excludes ops, help, and completion commands
// from discovery.
func (env *topLevelEnv) operations(t *testing.T) []string {
	t.Helper()
	keys := []string{"command tack"}
	var walk func(path []string)
	walk = func(path []string) {
		for _, name := range env.completions(t, path) {
			if name == "help" || name == "completion" || (len(path) == 0 && name == "ops") {
				continue
			}
			child := append(slices.Clone(path), name)
			keys = append(keys, "command tack "+strings.Join(child, " "))
			walk(child)
		}
	}
	walk(nil)
	for _, alias := range topLevelHiddenAliases {
		if _, output, err := env.run(t, alias, "--help"); err == nil {
			keys = append(keys, "command tack "+alias)
		} else {
			t.Logf("The --help check failed for hidden alias %s: %v\n%s", alias, err, output)
		}
	}
	slices.Sort(keys)
	return keys
}

// completions runs the server binary's __complete command for the supplied path
// and returns command names from its output.
func (env *topLevelEnv) completions(t *testing.T, path []string) []string {
	t.Helper()
	process := exec.CommandContext(t.Context(), env.binary, append(append([]string{"__complete"}, path...), "")...)
	process.Env = env.environment
	output, err := process.Output()
	if err != nil {
		t.Fatalf("Command completion failed; the test requires command discovery for path %v: %v", path, err)
	}
	var names []string
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "-") {
			continue
		}
		name, _, _ := strings.Cut(line, "\t")
		names = append(names, name)
	}
	return names
}

// requireStaticHelp runs a command with unreachable database addresses and
// requires a successful exit with "Usage:" in the output.
func (env *topLevelEnv) requireStaticHelp(t *testing.T, key string) {
	t.Helper()
	process := exec.CommandContext(t.Context(), env.binary, strings.Fields(strings.TrimPrefix(key, "command tack "))...)
	process.Env = append(slices.Clone(env.environment),
		"DATABASE_URL="+unreachableDatabaseURL, "AUDIT_OPERATOR_DSN="+unreachableDatabaseURL)
	output, err := process.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "Usage:") {
		t.Fatalf("The command failed or did not print 'Usage:'; the test requires static help with unreachable database addresses for %s: %v\n%s", key, err, output)
	}
}

// runServe starts the server with a new operator ID and waits for another
// startup log entry or a one-minute deadline. It sends SIGTERM, requires a
// successful exit, and returns the operator ID.
func (env *topLevelEnv) runServe(t *testing.T, args ...string) uuid.UUID {
	t.Helper()
	operatorID := uuid.Must(uuid.NewV7())
	process, output := env.command(t, operatorID, args...)
	started := env.logCount(t, serveStartedLog)
	if err := process.Start(); err != nil {
		t.Fatalf("The server process failed to start; the test requires a running process: %v", err)
	}
	deadline := time.Now().Add(time.Minute)
	for env.logCount(t, serveStartedLog) == started && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(time.Second)
	if err := process.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Sending SIGTERM failed; the test requires the server to accept the shutdown signal: %v", err)
	}
	if err := process.Wait(); err != nil {
		t.Fatalf("The server exited with an error; the test requires a successful exit after SIGTERM: %v\n%s", err, output.String())
	}
	return operatorID
}
