# Plan: env-config remediation, fail-loudly enforcement, and smoke test

## Context

Today's session has been bitten repeatedly by env-var failures that surfaced too late: `TACK_BACKUP_TEMPORAL_DB_PASSWORD` failing only when the backup orchestrator ran, then `YUGABYTE_PASSWORD` failing again on the next attempt, and earlier `KAFKA_CLUSTER_ID` blocking compose parse only because the operator happened to run a compose command. The 2026-05-09 audit-WAL drop bug had the same shape: silent degradation when an upstream condition wasn't met. The user's correction is sharp: **if a var is required, fail at startup, not later.**

Today's pattern in code: `config.Load()` runs once at `cmd/server/main.go:32-36` before any dispatch, but only `DATABASE_URL` has the `,required` tag in `internal/config/config.go:14`. Every other "required" check happens later — in `internal/ops/backup_run.go:33-41` for backup, in `cmd/server/seed.go:67-71` for seed, and many places where the code just branches on empty strings without surfacing anything. There is no `help` or `version` subcommand at all (they fall through to `runServer` per `cmd/server/main.go:63-93`), so an operator can't smoke-check config without starting the HTTP server.

This plan codifies the fail-loud-and-early pattern across the binary, compose, and docs, and adds a smoke-test subcommand so operators can inspect config without launching the full app.

## Decision summary

- **Stay with `caarlos0/env` v11.4.1.** Verified active maintenance through April 2026, no CVEs, handles `,required` at scale, zero dependencies. Researched alternatives (`kelseyhightower/envconfig`, `sethvargo/go-envconfig`) don't displace it.
- **Validation layered three ways**, in increasing severity (each enforces what the next one assumes):
  1. **Compose interpolation `:?`** for any var the container or service-startup actually needs. Fails before any container starts. Already used for `app`'s `DATABASE_URL` components and (in the working-tree edit awaiting commit) for tack-ops' `YUGABYTE_*`.
  2. **`caarlos0/env` `,required` tag** for any field the binary needs at runtime. Fails at `config.Load()` before dispatch. Today only `DATABASE_URL`; should expand to all truly-required vars.
  3. **Per-subcommand validation** for vars only some subcommands need (e.g., backup needs `TACK_BACKUP_TEMPORAL_DB_PASSWORD` but the long-running app doesn't). Validates against the parsed config struct after dispatch decides which subcommand. Failure exits before any work runs.
- **Help/version subcommands bypass `config.Load()` entirely** so they work even when the env is broken. They become the diagnostic entrypoint when something else is failing.
- **Smoke-test subcommand** that loads config in a non-failing mode and prints a status report (which required vars are set, which are missing, which defaults are in use). Lets operators inspect without booting the server.

## Approach

1. **Add `help`, `version`, and `status` subcommands that bypass config loading.**

   Refactor `cmd/server/main.go` so the dispatch checks `os.Args[1]` BEFORE calling `config.Load()`:

   ```go
   func main() {
       if len(os.Args) > 1 {
           switch os.Args[1] {
           case "help", "--help", "-h":
               printHelp()
               return
           case "version", "--version", "-v":
               printVersion()  // prints version.Commit, version.BuildTime, etc.
               return
           case "status":
               runStatus()  // load config in non-failing mode, print report
               return
           }
       }
       cfg, err := config.Load()
       // ...rest of existing dispatch
   }
   ```

   `printVersion` reads from the `internal/version` package (commit, buildTime, tag, dirty) and exits zero. `printHelp` prints subcommand list. `runStatus` is the smoke test (see step 4).

2. **Audit every field in `internal/config/config.go` against the rule: required if ever used in a path for normal operation; exception only for vars that modify runtime behavior temporarily.**

   Three enforcement tiers, mapped to where each var is used:
   - **Compose `:?` per-container**: every var the container's possible subcommands need, enforced at compose-parse time per service. tack-ops needs backup vars; app does not. Each service's `environment:` block enumerates its own required set.
   - **`,required` tag in config.go**: vars EVERY container needs (the shared baseline). Today only DATABASE_URL. Expand to YUGABYTE_PASSWORD, AUDIT_*_DSN, AUDIT_SIGNING_KEY_PATH (every container records audit and connects to YB).
   - **Per-subcommand `RequiredEnv() []string` validation**: vars only some subcommands need within a container. Failure consolidated into one error listing all missing vars at subcommand entry, before any work runs.

   Concrete tier 2 expansion: AUDIT_WRITER_DSN, AUDIT_READER_DSN, AUDIT_REDACTOR_DSN, AUDIT_SIGNING_KEY_PATH, YUGABYTE_PASSWORD. Remove the dev default on MEILI_MASTER_KEY (`tack-dev-meili-key-change-in-prod`) so it can't ship to production silently.

   Tier 3 examples: TACK_BACKUP_TEMPORAL_DB_PASSWORD, SEED_EMAIL/SEED_NAME, GHCR_USERNAME/GHCR_TOKEN, deploy-only vars.
   - `SEED_EMAIL`, `SEED_NAME` — only seed needs them (already validated at `cmd/server/seed.go:67-71`)
   - `GHCR_USERNAME`, `GHCR_TOKEN` — only `./server ops deploy` needs them; read via `os.Getenv` in `internal/ops/deploy.go`

3. **Subcommand-scoped required via embedded config structs.**

   Refactor `internal/config/config.go` into a `Base` struct with the shared-baseline required fields, then per-subcommand structs that embed `Base` and add their own `,required` fields. Each subcommand calls `env.Parse(&subcommandCfg)` at entry; caarlos0/env's native `,required` validation produces a single error listing every missing var.

   ```go
   // internal/config/config.go
   type Base struct {
       DatabaseURL      string `env:"DATABASE_URL,required"`
       YugabyteUser     string `env:"YUGABYTE_USER,required"`
       YugabytePassword string `env:"YUGABYTE_PASSWORD,required"`
       YugabyteDB       string `env:"YUGABYTE_DB,required"`
       AuditWriterDSN   string `env:"AUDIT_WRITER_DSN,required"`
       AuditReaderDSN   string `env:"AUDIT_READER_DSN,required"`
       AuditRedactorDSN string `env:"AUDIT_REDACTOR_DSN,required"`
       AuditSigningKeyPath string `env:"AUDIT_SIGNING_KEY_PATH,required"`
       // shared optionals with envDefault
   }

   // internal/ops/backup_config.go
   type BackupConfig struct {
       config.Base
       TemporalDBPassword string `env:"TACK_BACKUP_TEMPORAL_DB_PASSWORD,required"`
       TemporalDBUser     string `env:"TACK_BACKUP_TEMPORAL_DB_USER" envDefault:"temporal"`
       TemporalDBName     string `env:"TACK_BACKUP_TEMPORAL_DB_NAME" envDefault:"temporal"`
       // ...other backup-only fields
   }

   // In runBackupRun (replaces the existing checks at backup_run.go:33-41):
   var cfg BackupConfig
   if err := env.Parse(&cfg); err != nil {
       return fmt.Errorf("backup config: %w", err)
   }
   ```

   Same pattern for SeedConfig, DeployConfig, OpsRepairConfig, etc. Each subcommand owns its config type. The `Base` struct stays small (only what every subcommand needs).

4. **Smoke test: `/server status` subcommand.**

   Loads config in a NON-FAILING mode (every required becomes optional, just to enumerate). Walks every field and prints:

   - Var name
   - Value source: explicit env, default, or unset
   - Whether it's `,required` in config.go
   - Which subcommands explicitly check for it (built into a static map populated from each family's `RequiredEnv` function)
   - Compose-side decoration if applicable (`:?`, `:-`, undecorated)

   Output is a tab-separated or fixed-width table to stdout, exit 0 always (it's a smoke test, not a gate). Operators run it before deploys, after deploys, when something's wrong.

   To implement non-failing config load, either:
   - Use a separate config struct without `,required` tags purely for status inspection
   - Use `caarlos0/env`'s `env.ParseWithOptions` and capture errors as warnings instead of failures

   The latter is cleaner; `env.ParseWithOptions` supports `OnSet` callbacks and the parser can be invoked twice (once strict, once permissive).

5. **Compose enforcement: every `${VAR}` reference must be classified.**

   Audit `docker-compose.yml` for every `${VAR}` and apply:
   - **Required (binary or service can't function without it)**: `${VAR:?error message naming the var and where to set it}`
   - **Optional with default**: `${VAR:-default}`
   - **No bare `${VAR}` undecorated** (this is the silent-failure form: empty string passes through to the container, the binary then handles emptiness possibly silently)

   Specific known fixes after this rule:
   - All `AUDIT_*_DSN` references in app and audit-consumer become `:?` (currently undecorated)
   - `TACK_OPS_DATABASE_URL` becomes `:?` in tack-ops (currently undecorated)
   - `TACK_BACKUP_TEMPORAL_DB_PASSWORD` should NOT have a `:-temporal` fallback — that masks rotation. Make it `:?` and require it in `.env`.
   - `MEILI_MASTER_KEY` should NOT default to the dev value in compose. Make it `:?` and require explicit setting.

6. **Orphan cleanup in `/root/tack/.env`.**

   Three `AUDIT_*_PASSWORD` vars (READER, REDACTOR, WRITER) exist in `/root/tack/.env` but are unused (the DSNs are stored fully-composed). Either:
   - Remove from `.env` (their values are embedded in the DSNs already)
   - Or wire them into compose substitution to compose the DSNs at runtime (more flexible but requires DSN templating)

   Decision: remove from `.env` after confirming no script reads them.

7. **Double verification.**

   Two independent verification paths for the remediation, applied to every change:

   - **Unit-level**: A new test in `internal/config/config_test.go` asserts that every field with `,required` corresponds to a non-empty value in `.env.example`. Catches drift between code and operator-facing docs.
   - **Integration-level**: A new make target `make verify-env` runs `./server status` against a configured environment and verifies the output table matches expectations (every `,required` field is present, no orphans, no defaults masking secrets).

   Compose-side: a small Go test or shell script that parses `docker-compose.yml`, finds every `${VAR}` reference, and asserts that every undecorated reference is matched by a `,required` field in `config.go`. Catches the inverse drift.

8. **Retroactive Tack tickets.**

   File one ticket per remediation chunk so the work is tracked even if interleaved with other priorities:

   - **TACK-XXX (Library decision)**: Document caarlos0/env decision in CLAUDE.md / AGENTS.md. Trivial.
   - **TACK-XXX (Help/version/status subcommands)**: Add the three bypass-config subcommands. Medium-sized code change to cmd/server/main.go.
   - **TACK-XXX (Required tag expansion)**: Audit every config.go field, add `,required` to truly-required ones. Code change with risk (requires every deploy env to satisfy the new requirements).
   - **TACK-XXX (Per-subcommand RequiredEnv)**: Add the per-subcommand validation pattern. Refactor of existing one-off checks.
   - **TACK-XXX (Compose `:?` enforcement)**: Sweep docker-compose.yml, replace undecorated and `:-`-with-secret-default forms. Small but touches every service.
   - **TACK-XXX (Orphan AUDIT_*_PASSWORD cleanup)**: Remove unused vars from CT 117 `/root/tack/.env`.
   - **TACK-XXX (Verification harness)**: Implement the two test/verification paths from step 7.

9. **AGENTS.md codification.**

   Append a new section to `AGENTS.md` titled "Environment variable discipline." Wording (proposed):

   > Every env var the binary reads must be declared in `internal/config/config.go` with one of three statuses: `,required` (fails `config.Load()`), `envDefault:"..."` (silent default with WARN log line if it differs from production expectations), or per-subcommand validation (validated at subcommand entry, not at load).
   >
   > Every env var passed through a compose `environment:` block must use one of: `${VAR:?error message}` (loud-fail at compose parse), `${VAR:-default}` (silent fallback, only for true optionals), or a literal value with no interpolation (rare, only when the var should never be operator-overrideable).
   >
   > Undecorated `${VAR}` interpolation is forbidden because it silently substitutes empty strings into the container env, producing late-surfacing failures.
   >
   > Every required var must appear in `.env.example` with a placeholder value and a one-line comment describing what it's for.
   >
   > Help and version subcommands must work without `config.Load()`. Use them as the diagnostic entrypoint when something is broken.
   >
   > New env vars: declare in config.go, add to .env.example, add to every compose service that needs it via `${VAR:?...}` interpolation, document in this section if the variable is non-obvious.

## Critical files

- `cmd/server/main.go` (dispatch + config.Load + new help/version/status subcommands)
- `internal/config/config.go` (every field's tag — required vs default vs neither)
- `internal/ops/command.go` (per-subcommand RequiredEnv pattern; lines 28-64 dispatcher)
- `internal/ops/backup_run.go:33-41` (existing one-off checks; consolidate into RequiredEnv)
- `cmd/server/seed.go:67-71` (existing one-off checks; consolidate into RequiredEnv)
- `docker-compose.yml` (compose-side `:?` enforcement; sweep all `${VAR}` references)
- `.env.example` (every required var documented)
- `AGENTS.md` (codified rules section)

## Reusable existing utilities

- `caarlos0/env` v11.4.1 already imported in `internal/config/config.go:10`. Provides `,required`, `envDefault:`, custom parsers. No new library needed.
- `internal/version` package provides commit/buildTime/tag/dirty for the `version` subcommand.
- `internal/ops/command.go:28` (`RunCommand`) is the dispatcher to extend with per-family RequiredEnv calls.
- Compose's built-in `${VAR:?msg}` and `${VAR:-default}` syntax (no extra tooling needed).

## Verification

End-to-end test sequence after the remediation lands:

1. **`/server help`** prints subcommand list and exits 0 with no env set.
2. **`/server version`** prints `commit=...`, `build_time=...`, `tag=...`, `dirty=...` and exits 0 with no env set.
3. **`/server status`** prints the env table and exits 0 even when required vars are missing. Table correctly classifies each var (required-and-set, required-and-missing, default-in-use, optional-and-unset).
4. **`docker compose config -q`** with the production `.env` exits 0 (every `:?` interpolation is satisfied).
5. **`docker compose config -q`** with one required var temporarily removed from `.env` exits non-zero with a clear error pointing at the missing var.
6. **`/server` (no subcommand)** with one required var unset exits non-zero at `config.Load()` time with a clear error naming the missing var. Container does NOT start; `docker ps` shows the container restart-thrashing visibly.
7. **`./server ops backup`** with `TACK_BACKUP_TEMPORAL_DB_PASSWORD` unset (but other required vars present) exits non-zero at subcommand entry with a single-error message listing all missing backup vars. Does NOT start a partial backup.
8. **`make verify-env`** in CI exits 0 only if every `,required` field has a placeholder in `.env.example` and every undecorated compose interpolation is matched by a `,required` field.

## Sequencing

Today's deploy completion is independent of this plan. The deploy needs to: commit the YUGABYTE compose change, mirror to CT 117, retry backup, verify, restore-test. The smoke-test infrastructure adds value AFTER today's deploy because it would have made today's iterative env-discovery faster.

Recommended sequencing:

1. **Finish today's deploy** (separate work; the YUGABYTE compose change + retry).
2. **Land help/version/status subcommands first** (additive, no risk).
3. **Sweep config.go for required tag expansion** (medium risk; tests every deploy env).
4. **Sweep compose for `:?` enforcement** (low risk; compose-parse failures are loud and immediate).
5. **Add per-subcommand RequiredEnv** (refactor; consolidate existing checks).
6. **Add verification harness** (CI-only; doesn't affect runtime).
7. **Codify in AGENTS.md** as part of the same commit chain.
8. **Orphan AUDIT_*_PASSWORD cleanup** (last; lowest priority).

## Scope explicitly NOT in this plan

- Migrating to a different env-config library. Decision is to stay with caarlos0/env v11.4.1.
- Any change to the audit subsystem beyond making its DSN vars `,required`.
- Compose service shape (which services exist, what they do). Only their `environment:` blocks.
- Replacing `.env` with a different secret-management mechanism (1Password CLI, age-encrypted files, Vault). Tracked separately under TACK-237 token cleanup and similar.
