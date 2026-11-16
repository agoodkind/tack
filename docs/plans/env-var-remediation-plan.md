# Plan: env-config remediation, fail-loudly enforcement, and smoke test

## Context

The backup orchestrator failed after startup when `TACK_BACKUP_TEMPORAL_DB_PASSWORD` was missing, then failed again when `YUGABYTE_PASSWORD` was missing. Compose detected the missing `KAFKA_CLUSTER_ID` only when the operator ran a compose command. The 2026-05-09 audit-WAL drop bug also silently degraded when an upstream condition failed. Required variables must fail validation at startup, not during later work.

`config.Load()` runs once at `cmd/server/main.go:32-36` before dispatch, but only `DATABASE_URL` has the `,required` tag in `internal/config/config.go:14`. Backup checks required variables later in `internal/ops/backup_run.go:33-41`; seed checks them in [seed.go](../../internal/servercli/seed.go). Other code branches on empty strings without reporting them. The binary does not implement `help` or `version` subcommands. They fall through to `runServer` in `cmd/server/main.go:63-93`, preventing configuration inspection without starting the HTTP server.

Validate required variables before work starts and add a smoke-test subcommand to inspect configuration without launching the app.

## Decision summary

- Keep `caarlos0/env` v11.4.1. The library supports `,required` and does not add dependencies. Research through April 2026 found active maintenance and did not find CVEs or a reason to replace it with `kelseyhightower/envconfig` or `sethvargo/go-envconfig`.
- Apply validation at three levels, each enforcing the assumptions of the next:
  1. Compose interpolation `:?` checks variables required by a container or service startup before any container starts. The app already uses it for `DATABASE_URL` components; the working-tree edit adds it for tack-ops' `YUGABYTE_*`.
  2. `caarlos0/env` `,required` tags check fields required by every container at `config.Load()` before dispatch. Expand beyond `DATABASE_URL` to all shared required variables.
  3. Per-subcommand validation checks the parsed config after dispatch, before work. For example, backup requires `TACK_BACKUP_TEMPORAL_DB_PASSWORD`, but the long-running app does not.
- Help and version bypass `config.Load()` and remain available when configuration fails.
- The smoke-test subcommand loads configuration without failing and reports required variables, missing values, and defaults without starting the server.

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

   Apply each validation level to the variables it requires:
   - Compose `:?` checks every variable required by a container's possible subcommands at compose-parse time. tack-ops needs backup variables; app does not. Each service's `environment:` block lists its required set.
   - `,required` tags in config.go check variables every container needs. Expand beyond DATABASE_URL to YUGABYTE_PASSWORD, AUDIT_*_DSN, and AUDIT_SIGNING_KEY_PATH. Every container records audit and connects to YB.
   - Per-subcommand `RequiredEnv() []string` validation checks variables that only some subcommands need. Return one error listing all missing variables at subcommand entry, before work starts.

   Expand tier 2 to AUDIT_WRITER_DSN, AUDIT_READER_DSN, AUDIT_REDACTOR_DSN, AUDIT_SIGNING_KEY_PATH, and YUGABYTE_PASSWORD.

   Tier 3 includes TACK_BACKUP_TEMPORAL_DB_PASSWORD, SEED_EMAIL, and SEED_NAME. Only seed requires `SEED_EMAIL` and `SEED_NAME`, which `seedOp` already validates.

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

   Apply the pattern to SeedConfig, DeployConfig, OpsRepairConfig, and other subcommands. Each subcommand defines its config type. `Base` includes only fields every subcommand needs.

4. **Smoke test: `/server status` subcommand.**

   Load config without failing on required fields and inspect every field. Print:

   - Var name
   - Value source: explicit env, default, or unset
   - Whether it's `,required` in config.go
   - Which subcommands explicitly check for it (built into a static map populated from each family's `RequiredEnv` function)
   - Compose-side decoration if applicable (`:?`, `:-`, undecorated)

   Write a tab-separated or fixed-width table to stdout and always exit 0. Operators can inspect configuration before or after deploys and during failures.

   Implement non-failing config loading with either approach:
   - Use a separate config struct without `,required` tags purely for status inspection.
   - Use `caarlos0/env`'s `env.ParseWithOptions` and capture errors as warnings instead of failures.

   Prefer `env.ParseWithOptions`, which supports `OnSet` callbacks and permits two parser calls, one strict and one permissive.

5. **Classify every Compose `${VAR}` reference.**

   Audit `docker-compose.yml` for every `${VAR}` and apply:
   - Use `${VAR:?error message naming the var and where to set it}` when the binary or service requires the variable.
   - Use `${VAR:-default}` for optional variables with defaults.
   - Do not use undecorated `${VAR}`. It substitutes an empty string into the container, which the binary may handle silently.

   Apply these known fixes:
   - Change undecorated `AUDIT_*_DSN` references in app and audit-consumer to `:?`.
   - Change undecorated `TACK_OPS_DATABASE_URL` in tack-ops to `:?`.
   - Require `TACK_BACKUP_TEMPORAL_DB_PASSWORD` in `.env` with `:?` interpolation. Remove the `:-temporal` fallback that masks rotation.

6. **Preserve `AUDIT_*_PASSWORD` for audit role creation and rotation.**

   `./server ops audit seed-roles` reads `AUDIT_WRITER_PASSWORD`,
   `AUDIT_READER_PASSWORD`, and `AUDIT_REDACTOR_PASSWORD` to create or rotate LOGIN
   audit roles. `internal/config/config.go` declares them, and the `tack-ops`
   compose service receives them. Do not remove them.

7. **Verify every change through independent unit and integration checks.**

   - Add a test in `internal/config/config_test.go` that checks every `,required` field has a non-empty value in `.env.example`.
   - Add `make verify-env` to run `./server status` against a configured environment and check the table for every required field, unused variables, and defaults that mask secrets.

   Add a Go test or shell script that parses every `${VAR}` reference in `docker-compose.yml` and checks that each undecorated reference matches a `,required` field in `config.go`.

8. **Track remediation under TACK-266.**

   TACK-266 includes required-tag expansion, help/version/status subcommands,
   per-subcommand validation, compose `:?` enforcement, and verification.
   Exclude the proposed `AUDIT_*_PASSWORD` cleanup because `seed-audit-roles`
   uses those variables (see step 6).

9. **Codify the requirements in AGENTS.md.**

   Append a section titled "Environment variable discipline" with these proposed requirements:

   > Declare every environment variable the binary reads in `internal/config/config.go` as `,required`, `envDefault:"..."`, or subject to per-subcommand validation. `,required` fails `config.Load()`. A default requires a WARN log when it differs from production expectations. Per-subcommand validation runs at subcommand entry.
   >
   > Use `${VAR:?error message}`, `${VAR:-default}`, or a literal value without interpolation for every variable in a compose `environment:` block. Use defaults only for optional variables. Use literals only when operators must not override the value.
   >
   > Do not use undecorated `${VAR}` interpolation. It silently substitutes empty strings into container environments and delays failure.
   >
   > Include every required variable in `.env.example` with a placeholder and a one-line purpose comment.
   >
   > Help and version must work without `config.Load()` for diagnosis when configuration fails.
   >
   > Declare new variables in config.go, add them to .env.example, and add `${VAR:?...}` interpolation to each compose service that requires them. Explain non-obvious variables in this section.

## Critical files

- `cmd/server/main.go` (dispatch + config.Load + new help/version/status subcommands)
- `internal/config/config.go` (required, default, or neither for each field's tag)
- `internal/ops/command.go` (per-subcommand RequiredEnv pattern; lines 28-64 dispatcher)
- `internal/ops/backup_run.go:33-41` (existing one-off checks; consolidate into RequiredEnv)
- `seedOp` (existing one-off checks; consolidate into RequiredEnv)
- `docker-compose.yml` (compose-side `:?` enforcement; sweep all `${VAR}` references)
- `.env.example` (every required var documented)
- `AGENTS.md` (codified rules section)

## Reusable existing utilities

- `caarlos0/env` v11.4.1 in `internal/config/config.go:10` provides `,required`, `envDefault:`, and custom parsers without a new library.
- `internal/version` provides commit/buildTime/tag/dirty for the `version` subcommand.
- Extend `internal/ops/command.go:28` (`RunCommand`) with per-family RequiredEnv calls.
- Compose provides `${VAR:?msg}` and `${VAR:-default}` without extra tooling.

## Verification

Run these end-to-end checks after implementing the remediation:

1. `/server help` prints the subcommand list and exits 0 without environment variables.
2. `/server version` prints `commit=...`, `build_time=...`, `tag=...`, and `dirty=...`, then exits 0 without environment variables.
3. `/server status` prints the env table and exits 0 even when required variables are missing. Check each classification: required-and-set, required-and-missing, default-in-use, or optional-and-unset.
4. `docker compose config -q` exits 0 with the production `.env` and satisfies every `:?` interpolation.
5. `docker compose config -q` exits nonzero with one required variable temporarily removed from `.env` and identifies the missing variable.
6. `/server` without a subcommand exits nonzero at `config.Load()` when a required variable is unset and identifies it. The container must not start; `docker ps` shows repeated restart attempts.
7. `make verify-env` exits 0 in CI only when every `,required` field has a placeholder in `.env.example` and every undecorated compose interpolation matches a `,required` field.

## Sequencing

Deploy the YUGABYTE Compose change separately from the smoke-test infrastructure. Commit the change, mirror it to CT 117, retry the backup, verify it, and test restore.

Follow this sequence:

1. Finish the current deploy with the YUGABYTE compose change and retry.
2. Add help/version/status subcommands first.
3. Expand required tags in config.go and test every deploy environment.
4. Add compose `:?` enforcement.
5. Add per-subcommand RequiredEnv validation and consolidate existing checks.
6. Add the CI verification checks without changing runtime behavior.
7. Codify the requirements in AGENTS.md in the same commit chain.
8. Perform orphan AUDIT_*_PASSWORD cleanup last, at the lowest priority.

## Scope explicitly NOT in this plan

- Do not replace caarlos0/env v11.4.1 with another configuration library.
- Do not change the audit subsystem beyond making its DSN variables `,required`.
- Change only compose `environment:` blocks, not services or their behavior.
- Do not replace `.env` with 1Password CLI, age-encrypted files, Vault, or another secret-management mechanism. TACK-237 token cleanup and related work track that separately.
