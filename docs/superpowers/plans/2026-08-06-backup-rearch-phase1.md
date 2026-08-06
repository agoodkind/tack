# Backup Rearch Phase 1 (No New Guests) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the dump-based backup machinery, bound every service's memory, add an app health endpoint, single-home the ledger signer, and raise the audit producer's delivery budget, all on the existing guests.

**Architecture:** Spec is `docs/superpowers/specs/2026-08-06-backup-rearch-design.md`. Phase 1 touches the tack repo (Go + docker-compose.yml) and one configs-repo template (traefik). No new guests; no topology change. Every change lands on QA (tack_qa deploy) before production.

**Tech Stack:** Go 1.x (cobra, franz-go), docker compose, Jinja2 (configs repo), YugabyteDB 2024.2.8.0 gflags.

## Global Constraints

- Build only with `make build`; format with `make fmt`. Never raw `go build`.
- The deadcode gate does whole-program reachability from `cmd/`: deletions of callers and callees must land in the same commit.
- Integration tests gate on `AUDIT_CHAIN_TEST_DSN`; unit tests must run DB-free.
- Commit style: imperative subject, `Co-authored-by: Claude <noreply@anthropic.com>` trailer, `git commit -S`.
- The worktree for tack work is a linked worktree; never edit the primary checkout.
- Per working agreements: the brief is a hypothesis. Verify every cited line against current source before editing; if reality disagrees, stop and report.

---

### Task 1: Guard the bare `ops backup` command and delete the dump machinery

**Files:**
- Modify: `internal/ops/cli_backup.go` (bare RunE at :18-20; verify subcommand at :23-30)
- Delete: `internal/ops/backup_run.go`, `internal/ops/backup_yugabyte.go`, `internal/ops/backup_temporal.go`, `internal/ops/backup_meilisearch.go`, `internal/ops/backup_manifest.go`, `internal/ops/backup_manifest_test.go`, `internal/ops/backup_verify.go`, `internal/ops/backup_verify_test.go`, `internal/ops/backup_fdb.go` (its only caller was `runBackupRun`; `fdb-continuous-init` keeps its own entry point in `backup_fdb_continuous.go`)
- Modify: `internal/ops/backup_fdb_continuous.go` (the `backupCtx` struct currently defined in `backup_run.go` is SHARED: `RunBackupFDBContinuousInit` constructs one at :40-46 and `ensureFDBContinuousSession`, `fdbBackupStartArgs`, and `fdbBackupActive` take it. Move the `backupCtx` type definition, with its doc comment, into `backup_fdb_continuous.go` unchanged; delete only `runBackupRun`, `newBackupCtx`, and `updateLatestPointer` with their file.)
- Modify: `internal/ops/backup_fdb_test.go` (delete ONLY `TestRunBackupFDBRequiresObjectStore` at :12-25, whose subject dies; the four remaining tests cover surviving continuous-session logic and stay.)
- Modify: `internal/ops/backup_buckets.go` (stop creating the never-written `tack-audit-archive` bucket; keep `tack-backups`)
- Test: `internal/ops/cli_backup_test.go` (new)

**Interfaces:**
- Consumes: existing cobra tree built by `backupCommand(f *cli.Factory)`.
- Produces: `backupCommand` unchanged in name and signature; the bare command returns an error. Subcommands that remain: `buckets-init`, `yb-pitr-init`, `yb-snapshot-export`, `restore-drill`, `fdb-continuous-init`.

- [ ] **Step 1: Write the failing test**

```go
package ops

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/cli"
)

// TestBareBackupCommandRefuses locks in that `ops backup` with no
// subcommand runs nothing and exits nonzero. The 2026-08-05 S0 was caused
// by the bare command silently running a full production snapshot.
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

// TestBackupSubcommandsPresent locks the surviving subcommand set so a
// rename or accidental deletion fails loudly.
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

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/ops/ -run 'TestBareBackupCommand|TestBackupSubcommands' -v` (via `make` test target if one wraps it)
Expected: FAIL. The bare command currently calls `runBackupRun` and `verify` exists.

- [ ] **Step 3: Implement**

In `cli_backup.go`, replace the bare RunE and drop the verify block:

```go
RunE: func(cmd *cobra.Command, _ []string) error {
	return fmt.Errorf("ops backup requires a subcommand (%s); the bare command deliberately runs nothing", strings.Join(backupSubcommandNames(cmd), ", "))
},
```

Add the helper in the same file:

```go
// backupSubcommandNames lists the registered subcommands so the refusal
// error always matches reality.
func backupSubcommandNames(cmd *cobra.Command) []string {
	names := make([]string, 0, len(cmd.Commands()))
	for _, sub := range cmd.Commands() {
		names = append(names, strings.Fields(sub.Use)[0])
	}
	return names
}
```

Delete the files listed above. In `backup_buckets.go`, remove the audit bucket from the creation list (the constant or slice naming `tack-audit-archive`; verify current shape at `:38`). Update its short help text in `cli_backup.go` from plural buckets to singular.

- [ ] **Step 4: Run the full gate**

Run: `make build && make fmt`
Expected: PASS. Deadcode gate confirms nothing unreachable remains (deleting `backup_run.go` removes the callers of the deleted dumpers in one commit).

- [ ] **Step 5: Run the new tests**

Run: `go test ./internal/ops/ -run 'TestBareBackupCommand|TestBackupSubcommands' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add -A internal/ops
git commit -S -m "Guard bare ops backup and delete the dump-based backup machinery

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 2: Bound YugabyteDB memory explicitly

**Files:**
- Modify: `docker-compose.yml` (yugabyte service `command`, the `--tserver_flags` line at :120)

**Interfaces:**
- Consumes: the yugabyted start wrapper in the compose command block.
- Produces: tserver flags `memory_limit_hard_bytes=8589934592`, `db_block_cache_size_bytes=2147483648`, alongside the existing `ysql_pg_conf_csv=max_connections=100`.

- [ ] **Step 1: Edit the flags**

Replace the `--tserver_flags` line in the yugabyte service command:

```yaml
          --tserver_flags="ysql_pg_conf_csv=max_connections=100,memory_limit_hard_bytes=8589934592,db_block_cache_size_bytes=2147483648"
```

Add a comment above it in the compose file:

```yaml
        # Hard memory bound: 8 GiB process ceiling, 2 GiB block cache.
        # Replaces use_memory_defaults_optimized_for_ysql self-sizing, which
        # sizes from guest RAM and let a full-table scan grow the tserver to
        # 13.7 GiB and starve the guest's page cache (2026-08-05 S0).
```

- [ ] **Step 2: Verify the render**

Run: `docker compose config 2>/dev/null | sed -n '/tserver_flags/p'` in the worktree
Expected: one line containing all three flags.

- [ ] **Step 3: Commit**

```bash
git add docker-compose.yml
git commit -S -m "Bound yb-tserver memory with explicit hard-limit and block-cache flags

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 3: Per-service memory limits in compose

**Files:**
- Modify: `docker-compose.yml` (every long-running service)

**Interfaces:**
- Produces: `mem_limit` on each service. Caps, not reservations; the sum may exceed a guest's RAM because each cap only stops one service from claiming everything (QA guests are smaller than production).

- [ ] **Step 1: Add limits**

Add `mem_limit` directly under `restart: unless-stopped` for each service:

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
| clickhouse | 2g |
| audit-consumer | 1g |

yugabyte's 10g cap sits above the 8 GiB process bound from Task 2 so the cap is the backstop, not the operating limit.

- [ ] **Step 2: Verify the render**

Run: `docker compose config 2>/dev/null | sed -n '/mem_limit/p'`
Expected: eleven lines.

- [ ] **Step 3: Commit**

```bash
git add docker-compose.yml
git commit -S -m "Add per-service memory limits to the compose stack

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 4: App health endpoint

**Files:**
- Create: `cmd/server/health.go`
- Test: `cmd/server/health_test.go`
- Modify: `cmd/server/server_runtime.go` (mux registration block at :141-158)

**Interfaces:**
- Consumes: the datastore handles available where the mux is built (verify current names in `server_runtime.go`; the runtime graph carries the FDB client and pgx pool).
- Produces: `GET /healthz` returning 200 with body `ok` when every probe passes, 503 with the failing probe's name otherwise. `newHealthHandler(probes map[string]func(context.Context) error) http.Handler`.

- [ ] **Step 1: Write the failing test**

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

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/server/ -run TestHealthz -v`
Expected: FAIL, `newHealthHandler` undefined.

- [ ] **Step 3: Implement**

`cmd/server/health.go`:

```go
package main

import (
	"context"
	"net/http"
	"time"
)

// healthProbeTimeout bounds each datastore probe so a hung store makes the
// endpoint report unhealthy instead of hanging the proxy's health check.
const healthProbeTimeout = 2 * time.Second

// newHealthHandler serves GET /healthz for the ingress proxy's active
// health check. 200 means every named probe succeeded within its timeout;
// 503 names the first failing probe. Probes must be cheap reads.
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

In `server_runtime.go`, register it beside the existing `/mcp` registrations with real probes: the pgx pool's `Ping`, and an FDB read-version fetch via the existing client handle. Use the names `"yugabyte"` and `"fdb"`. Meilisearch and temporal are not probed: the app degrades without them, and a health check must reflect ability to serve, not full stack health.

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/server/ -run TestHealthz -v` then `make build`
Expected: PASS, build clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/server
git commit -S -m "Add /healthz endpoint probing yugabyte and fdb for ingress health checks

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 5: Single-home the ledger signer in the audit-consumer

**Files:**
- Modify: `cmd/server/audit_runtime.go` (notarizer construction and Start at :29-32; gate at :169)
- Test: modify existing audit runtime tests in `cmd/server/` that assert notarizer startup (find by searching the package for notarizer references; update them to assert the app does NOT start one)

**Interfaces:**
- Consumes: nothing new.
- Produces: the app process never constructs or starts `notarizer`; the audit-consumer (`cmd/audit-consumer`, untouched) remains the only signer. The app keeps the signing-key mount unused until the deploy stops mounting it (configs-side, phase 2).

- [ ] **Step 1: Write or update the failing test**

```go
// TestAppRuntimeStartsNoNotarizer locks in that the app process never
// signs the ledger. The audit-consumer is the single signer; two signers
// with per-host keys wrote duplicate notarizations under different
// identities.
func TestAppRuntimeStartsNoNotarizer(t *testing.T) {
	// Construct the audit runtime exactly as main does, with a signing key
	// path and writer DSN set, and assert the returned runtime has no
	// notarizer component. Adapt the constructor call to the current
	// signature in audit_runtime.go; the assertion is on the returned
	// struct's fields, not on log output.
	rt := buildTestAuditRuntime(t)
	if rt.notarizer != nil {
		t.Fatal("app must not run a notarizer; the audit-consumer is the single signer")
	}
}
```

The helper `buildTestAuditRuntime` wraps the existing construction path with test doubles already used by the package's tests; mirror the neighboring test setup in `cmd/server` when writing it.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/server/ -run TestAppRuntimeStartsNoNotarizer -v`
Expected: FAIL, notarizer present.

- [ ] **Step 3: Implement**

In `audit_runtime.go`, delete the notarizer construction and `Start` call (verified at :29-32) and the fields that hold it. Keep the writer DSN and recorder wiring untouched; the app still produces audit events. If a comment nearby claims the app notarizes, fix it in the same edit.

- [ ] **Step 4: Run the package tests and the build**

Run: `go test ./cmd/server/ -v` then `make build`
Expected: PASS; deadcode may now flag notarizer helpers only reachable from the app; the notarizer itself stays reachable from `cmd/audit-consumer`.

- [ ] **Step 5: Commit**

```bash
git add cmd/server
git commit -S -m "Remove the notarizer from the app process; audit-consumer is the single signer

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 6: Raise the audit producer delivery budget

**Files:**
- Modify: `internal/audit/kafka_recorder.go` (default at :59-61)
- Test: existing config-default test in `internal/audit` (extend or add)

**Interfaces:**
- Produces: default produce timeout 15s (env `AUDIT_KAFKA_PRODUCE_TIMEOUT` still overrides). Spec basis: a hard broker loss costs up to 9s lease expiry plus up to 5s client metadata refresh; 10s can expire inside that window.

- [ ] **Step 1: Write the failing test**

```go
func TestProduceTimeoutDefaultCoversHardBrokerLoss(t *testing.T) {
	cfg := recorderConfigFromEnv(map[string]string{}) // adapt to the actual config constructor
	if cfg.ProduceTimeout < 15*time.Second {
		t.Fatalf("default produce timeout %v below the 14s hard-loss window", cfg.ProduceTimeout)
	}
}
```

Adapt the constructor name to the current parsing path at `kafka_recorder.go:59-61`; assert through the exported surface the package already tests.

- [ ] **Step 2: Run to verify failure**

Expected: FAIL at 10s.

- [ ] **Step 3: Implement**

Change the default from `10 * time.Second` to `15 * time.Second`, with a comment:

```go
// 15s: a hard broker loss costs up to broker.session.timeout.ms (9s)
// before fencing plus up to the client's 5s metadata refresh floor before
// recovery; 10s expired inside that window and surfaced spurious Record
// errors during single-broker failures.
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/audit/ -run TestProduceTimeout -v && make build`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/audit
git commit -S -m "Raise the default audit produce timeout to 15s to cover hard broker loss

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 7: Traefik active health check (configs repo)

**Files:**
- Modify (configs repo, its own worktree off configs main): `traefik/dynamic/routes.yml.j2` (`tack-service` at :175-178)

**Interfaces:**
- Consumes: `GET /healthz` from Task 4, deployed to the guest.
- Produces: an active health check on the single existing upstream; the second upstream arrives in phase 6.

- [ ] **Step 1: Edit the service block**

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

- [ ] **Step 2: Validate the template renders**

Run the configs repo's lint (`go run goodkind.io/configs/cmd/configs lint`) from the configs worktree.
Expected: PASS.

- [ ] **Step 3: Commit (configs worktree), open PR per repo convention**

```bash
git add traefik/dynamic/routes.yml.j2
git commit -S -m "Add an active healthCheck to tack-service in the traefik routes template

Co-authored-by: Claude <noreply@anthropic.com>"
```

Sequencing: merge and deploy only after Task 4 is deployed to the guest, or the proxy will mark the upstream down.

---

### Task 8: QA deploy and verification, then production

**Files:** none (operational).

- [ ] **Step 1: Merge the tack branch through the standard gates** (two-lens GPT review iterated to approved, CI green), then QA deploy:

```bash
go run goodkind.io/configs/cmd/configs deploy deploy-tack --limit tack_qa_servers --extra-var tack_commit=main
```

- [ ] **Step 2: Verify on QA** (read-only script, one SSH):
  - `docker compose run --rm tack-ops ops backup` exits nonzero and names the subcommands.
  - The tserver command line shows `memory_limit_hard_bytes=8589934592`.
  - `docker inspect` shows the per-service memory caps.
  - `curl http://127.0.0.1:8000/healthz` inside the app netns returns 200 `ok`.
  - The app log contains no notarizer lines; the consumer log still notarizes each minute.
  - The consumer env shows the 15s default when `AUDIT_KAFKA_PRODUCE_TIMEOUT` is unset.

- [ ] **Step 3: Deploy the traefik change** (configs PR merged, proxy deploy) and verify the proxy reports the upstream healthy.

- [ ] **Step 4: Production deploy, with operator approval, same verification set.** Production database restart caveat: the new tserver flags apply on container recreate, which restarts the database once; this also releases the 13.7 GiB ballooned process from the 2026-08-05 incident. State this before running.

---

## Self-review

Spec coverage for phase 1: dump deletion and bare-command guard (Task 1), explicit database memory limits (Task 2), per-service limits (Task 3), health endpoint (Task 4), signer single-homing (Task 5), producer budget (Task 6), the traefik probe that makes the health endpoint load-bearing (Task 7), QA-then-prod rollout (Task 8). Signing-key vaulting and the hostname column ride with phase 2 (the spec's app-tier section), not phase 1's list. Type consistency: `newHealthHandler` is defined in Task 4 and consumed only by Task 4's registration; no cross-task symbols otherwise. No placeholders; the two "adapt to current signature" notes are verification instructions under the working agreement, each anchored to a cited line.
