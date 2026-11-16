# Implement backup resilience without new guests

Use `superpowers:subagent-driven-development` or `superpowers:executing-plans`
to implement tasks and track the checkboxes. Follow the
[backup design](../specs/2026-08-06-backup-rearch-design.md).

Phase 1 deletes dump-based backups, limits service memory, adds app health
checks, restricts ledger signing to audit-consumer, and raises the producer
budget. It changes Tack Go and Compose plus one configs Traefik template.
It does not add guests or change topology. Validate on QA before production.
Use Go 1.x, cobra, franz-go, Docker Compose, Jinja2, and YugabyteDB
2024.2.8.0 gflags.

## Constraints

- Build with `make build` and format with `make fmt`. Do not call `go build`.
- Delete callers and callees in the same commit. The deadcode check analyzes
  production calls from `cmd/`.
- Gate integration tests on `AUDIT_CHAIN_TEST_DSN`; keep unit tests database-free.
- Sign commits with `git commit -S`. Use an imperative subject and include
  `Co-authored-by: Claude <noreply@anthropic.com>`.
- Edit a linked worktree rather than the primary checkout.
- Verify cited source lines before editing. Stop and report any disagreement
  between current source and this plan.

## Task 1: Guard bare backup and delete dumps

Change `internal/ops/cli_backup.go` to reject bare `ops backup` and remove its
verify subcommand. Delete these files:

- Delete `internal/ops/backup_run.go`, `backup_yugabyte.go`, `backup_temporal.go`,
  and `backup_meilisearch.go`.
- Delete `internal/ops/backup_manifest.go`, `backup_manifest_test.go`,
  `backup_verify.go`, and `backup_verify_test.go`.
- Delete `internal/ops/backup_fdb.go`, which only `runBackupRun` invokes.
  Keep continuous initialization in `backup_fdb_continuous.go`.

Before deletion, preserve shared declarations. Copy `backupCtx`, including its
comment, unchanged into `internal/ops/backup_fdb_continuous.go`.
`RunBackupFDBContinuousInit` constructs it at lines 40 to 46;
`ensureFDBContinuousSession`, `fdbBackupStartArgs`, and `fdbBackupActive` use it.
Delete only `runBackupRun`, `newBackupCtx`, and `updateLatestPointer` with their file.
Copy `yugabyteBackupContainer` and its comment unchanged from
`backup_yugabyte.go:13` into `backup_yb_snapshot_copy.go`, which uses it at
lines 32 and 92.

Delete only `TestRunBackupFDBRequiresObjectStore` from
`internal/ops/backup_fdb_test.go:12-25`. Keep the four continuous-session tests.
In `backup_buckets.go`, stop creating `tack-audit-archive` and preserve
`tack-backups`. Remove `BackupTemporalDBPass`, `BackupTemporalDBUser`, and
`BackupTemporalDBName` from `internal/config/config.go` with their dump-only
consumers. Revise backup comments at lines 106 to 109 and 115 to 117 for the
remaining subcommands. Correct any comment that these deletions make false in
the same commit and list it in the result file.

Preserve `backupCommand(f *cli.Factory)` and these subcommands:
`buckets-init`, `yb-pitr-init`, `yb-snapshot-export`, `restore-drill`, and
`fdb-continuous-init`.

- [ ] Add failing tests in `internal/ops/cli_backup_test.go`:

```go
package ops

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/cli"
)

// TestBareBackupCommandRefuses verifies refusal without a subcommand.
// Bare backup silently ran a full production snapshot on 2026-08-05.
func TestBareBackupCommandRefuses(t *testing.T) {
	cmd := backupCommand(&cli.Factory{})
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("bare `ops backup` must return an error, ran something instead")
	}
	if !strings.Contains(err.Error(), "subcommand") {
		t.Fatalf("error must direct the operator to a subcommand, got: %v", err)
	}
}

// TestBackupSubcommandsPresent verifies the surviving subcommand set.
func TestBackupSubcommandsPresent(t *testing.T) {
	cmd := backupCommand(&cli.Factory{})
	want := map[string]bool{
		"buckets-init": false, "yb-pitr-init": false,
		"yb-snapshot-export": false, "restore-drill": false,
		"fdb-continuous-init": false,
	}
	for _, sub := range cmd.Commands() {
		name := strings.Fields(sub.Use)[0]
		if _, ok := want[name]; ok {
			want[name] = true
		}
		if name == "verify" {
			t.Fatal("verify subcommand must be deleted with the manifest machinery")
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("subcommand %s missing", name)
		}
	}
}
```

- [ ] Run `go test ./internal/ops/ -run 'TestBareBackupCommand|TestBackupSubcommands' -v`
  through a wrapping make target if available. Require failure while bare
  backup invokes `runBackupRun` and verify still exists.
- [ ] Replace bare RunE and delete the verify registration:

```go
RunE: func(cmd *cobra.Command, _ []string) error {
	return fmt.Errorf("ops backup requires a subcommand (%s); the bare command deliberately runs nothing", strings.Join(backupSubcommandNames(cmd), ", "))
},
```

Add the helper in that file:

```go
// backupSubcommandNames lists registered subcommands for the refusal error.
func backupSubcommandNames(cmd *cobra.Command) []string {
	names := make([]string, 0, len(cmd.Commands()))
	for _, sub := range cmd.Commands() {
		names = append(names, strings.Fields(sub.Use)[0])
	}
	return names
}
```

- [ ] Complete deletions and remove the audit bucket from the creation list
  after checking its current form at `backup_buckets.go:38`. Change the short
  help text in `cli_backup.go` from plural buckets to singular.
- [ ] Run `make build && make fmt` and require success, including deadcode.
- [ ] Rerun the focused tests and require success.
- [ ] Commit:

```bash
git add -A internal/ops
git commit -S -m "Guard bare ops backup and delete the dump-based backup machinery

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 2: Set explicit YugabyteDB memory limits

- [ ] Replace the Yugabyte service `--tserver_flags` line in
  `docker-compose.yml:120` with:

```yaml
          --tserver_flags="ysql_pg_conf_csv=max_connections=100,memory_limit_hard_bytes=8589934592,db_block_cache_size_bytes=2147483648"
```

Add this comment above the flags:

```yaml
        # Limit process memory to 8 GiB and block cache to 2 GiB.
        # The default use_memory_defaults_optimized_for_ysql sizes from guest
        # RAM. On 2026-08-05, a full-table scan grew tserver to 13.7 GiB
        # and exhausted the guest's available page cache.
```

- [ ] Run `docker compose config 2>/dev/null | sed -n '/tserver_flags/p'`
  in the worktree. Require one line with all three flags.
- [ ] Commit:

```bash
git add docker-compose.yml
git commit -S -m "Bound yb-tserver memory with explicit hard-limit and block-cache flags

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 3: Limit each service's memory

Add `mem_limit` directly under `restart: unless-stopped` on every long-running
service in `docker-compose.yml`. These limits cap individual services rather
than reserve memory, and their sum may exceed guest RAM. QA guests are smaller
than production guests.

Use these exact caps:

| service | mem_limit |
|---|---|
| app | 1g |
| yugabyte | 10g |
| fdb | 2g |
| fdb-backup-agent | 512m |
| meilisearch | 2g |
| temporal-db | 1g |
| temporal | 1g |
| temporal-ui | 512m |
| kafka | 2g |
| audit-consumer | 1g |

YugabyteDB's 10g container cap exceeds the 8 GiB process limit from Task 2.
Use the process limit during normal operation and the container cap as the
outer limit.

- [ ] Add the limits and run
  `docker compose config 2>/dev/null | sed -n '/mem_limit/p'`. Require ten lines.
- [ ] Commit:

```bash
git add docker-compose.yml
git commit -S -m "Add per-service memory limits to the compose stack

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 4: Add datastore health checks

Create `cmd/server/health.go` and `health_test.go`. Register the handler beside
MCP in [server_runtime.go](../../../internal/servercli/server_runtime.go).
Use the graph's FDB client and
pgx pool after checking the current declarations.

- [ ] Add failing tests:

```go
package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestHealthzAllProbesPass(t *testing.T) {
	h := newHealthHandler(map[string]func(context.Context) error{
		"yugabyte": func(context.Context) error { return nil },
		"fdb":      func(context.Context) error { return nil },
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestHealthzFailingProbeReturns503(t *testing.T) {
	h := newHealthHandler(map[string]func(context.Context) error{
		"yugabyte": func(context.Context) error { return errors.New("down") },
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 503 {
		t.Fatalf("want 503, got %d", rec.Code)
	}
}
```

- [ ] Run `go test ./cmd/server/ -run TestHealthz -v`. Require compilation
  failure for undefined `newHealthHandler`.
- [ ] Implement `cmd/server/health.go`:

```go
package main

import (
	"context"
	"net/http"
	"time"
)

// healthProbeTimeout limits the duration of each datastore check.
const healthProbeTimeout = 2 * time.Second

// newHealthHandler serves the proxy's GET /healthz checks.
// It returns 200 after every probe succeeds or 503 identifying the first
// failing probe. Probes must perform inexpensive reads.
func newHealthHandler(probes map[string]func(context.Context) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, probe := range probes {
			ctx, cancel := context.WithTimeout(r.Context(), healthProbeTimeout)
			err := probe(ctx)
			cancel()
			if err != nil {
				http.Error(w, name+" unhealthy", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}
```

- [ ] Register real probes using pgx `Ping` and an FDB read-version fetch.
  Use `"yugabyte"` and `"fdb"` as probe keys. Do not probe Meilisearch or
  Temporal; the app can serve with degraded behavior without them.
- [ ] Run `go test ./cmd/server/ -run TestHealthz -v`, then `make build`.
  Require success.
- [ ] Commit:

```bash
git add cmd/server
git commit -S -m "Add /healthz endpoint probing yugabyte and fdb for ingress health checks

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 5: Restrict notarization to audit-consumer

Remove notarizer construction and `Start` at `cmd/server/audit_runtime.go:29-32`
and its gate at line 169. Update existing runtime tests to require that the app
does not start a notarizer. Keep `cmd/audit-consumer` unchanged as the sole
signer. Keep the app's unused signing-key mount until configs removes it in phase 2.

- [ ] Add or update the failing test using neighboring test setup and the
  package's existing doubles:

```go
// TestAppRuntimeStartsNoNotarizer verifies that only audit-consumer signs.
// Two signers using per-host keys previously wrote duplicate notarizations
// under different identities.
func TestAppRuntimeStartsNoNotarizer(t *testing.T) {
	// Construct the normal audit runtime with a signing-key path and writer
	// DSN. Adapt the constructor to audit_runtime.go and inspect the returned
	// fields rather than log output.
	rt := buildTestAuditRuntime(t)
	if rt.notarizer != nil {
		t.Fatal("app must not run a notarizer; the audit-consumer is the single signer")
	}
}
```

- [ ] Run `go test ./cmd/server/ -run TestAppRuntimeStartsNoNotarizer -v`
  and require failure while the notarizer exists.
- [ ] Delete construction, startup, and storing fields. Preserve the writer
  DSN and recorder setup for app audit events. Correct stale signer comments.
- [ ] Run `go test ./cmd/server/ -v`, then `make build`. Delete any app-only
  helpers that deadcode reports; preserve the consumer's notarizer calls.
- [ ] Commit:

```bash
git add cmd/server
git commit -S -m "Remove the notarizer from the app process; audit-consumer is the single signer

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 6: Raise the producer delivery budget

Change the default at `internal/audit/kafka_recorder.go:59-61` to 15s while
preserving the `AUDIT_KAFKA_PRODUCE_TIMEOUT` override. A broker failure can
require 9s for lease expiry and 5s for client metadata refresh. The 10s budget
can expire within that 14s window.

- [ ] Extend the config-default test, adapting the constructor to current code:

```go
func TestProduceTimeoutDefaultCoversHardBrokerLoss(t *testing.T) {
	cfg := recorderConfigFromEnv(map[string]string{}) // Use the current config constructor.
	if cfg.ProduceTimeout < 15*time.Second {
		t.Fatalf("default produce timeout %v below the 14s hard-loss window", cfg.ProduceTimeout)
	}
}
```

- [ ] Require failure at 10s through the exported config interface the package
  already tests.
- [ ] Change `10 * time.Second` to `15 * time.Second` with this comment:

```go
// Broker failure recovery can require broker.session.timeout.ms (9s)
// before fencing and the client's 5s minimum metadata refresh interval.
// The 15s budget includes both; the old 10s budget caused Record errors
// during single-broker failures.
```

- [ ] Run `go test ./internal/audit/ -run TestProduceTimeout -v && make build`
  and require success.
- [ ] Commit:

```bash
git add internal/audit
git commit -S -m "Raise the default audit produce timeout to 15s to cover hard broker loss

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 7: Configure Traefik health checks

Use a separate configs worktree from main. Modify `tack-service` at
`traefik/dynamic/routes.yml.j2:175-178`:

```yaml
    tack-service:
      loadBalancer:
        servers:
          - url: "h2c://[{{ service_mapping.tack.ipv6 }}]:8000"
        healthCheck:
          path: /healthz
          interval: 10s
          timeout: 3s
```

- [ ] Run `go run goodkind.io/configs/cmd/configs lint` from that worktree
  and require success.
- [ ] Commit and open a PR under configs conventions:

```bash
git add traefik/dynamic/routes.yml.j2
git commit -S -m "Add an active healthCheck to tack-service in the traefik routes template

Co-authored-by: Claude <noreply@anthropic.com>"
```

Deploy Task 4 before merging and deploying this change. Otherwise the proxy
marks the sole upstream unhealthy. Phase 6 adds the second upstream.

## Task 8: Verify QA before production

- [ ] Require two-lens GPT review until approval and passing CI before merging
  Tack. Deploy QA:

```bash
go run goodkind.io/configs/cmd/configs deploy deploy-tack --limit tack_qa_servers --extra-var tack_commit=main
```

- [ ] Use one read-only SSH session to verify the QA deployment:
  - Require `docker compose run --rm tack-ops ops backup` to exit nonzero and
    list its subcommands.
  - Require `memory_limit_hard_bytes=8589934592` in the tserver command line.
  - Read every service cap with `docker inspect`.
  - Require `curl http://127.0.0.1:8000/healthz` in the app network namespace
    to return 200 `ok`.
  - Confirm that the app does not log notarization and the consumer still signs
    every minute.
  - Confirm the 15s consumer environment default when
    `AUDIT_KAFKA_PRODUCE_TIMEOUT` lacks an override.
- [ ] Merge and deploy the configs proxy change. Require a healthy upstream.
- [ ] Request production approval and repeat the complete verification.
  Before deployment, explain that applying new tserver flags recreates the
  container and restarts the database once, also releasing the 13.7 GiB process
  from the 2026-08-05 incident.

## Check task consistency

Tasks 1 through 8 cover dump deletion, database limits, service caps, health,
signer restriction, delivery budget, proxy probes, and release verification.
The design assigns signing-key vaulting and the hostname column to phase 2.
`newHealthHandler` has one definition and registration in Task 4. Verify the
current constructor signatures in Tasks 5 and 6 instead of treating their test
examples as final call signatures.
