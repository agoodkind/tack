# Ops consolidation plan: shell scripts to `./server ops` subcommands

Date: 2026-05-09
Status: planning, not yet implemented
Author: planning subagent
Audience: operator (Alex) plus any implementing agent
Constraint summary: no new shell scripts, no source rsync to production, all
tests and verification gates run inside Docker.

This plan describes how to fold the current backup, verify, restore-test, and
deploy shell tooling into the existing Tack monolith under `./server ops *`,
and how to ship a built image instead of synced source. It is meant to be
read top to bottom by whoever implements it. Where I am uncertain, I say so
and list the open question for the operator at the end.

## 1. Goals and non-goals

### Goals

- Move every operator-facing shell script that currently lives under
  `scripts/backup*.sh` into a Go subcommand of the existing `./server ops`
  family. The same monolith binary that runs the HTTP server also runs ops.
- Replace `make deploy` with `./server ops deploy`. The deploy step ships a
  built container image to the production host. It does not rsync source.
- Reduce the production filesystem footprint to the smallest practical set:
  one `docker-compose.yml`, one `.env`, host bind-mounts. No `/root/tack`
  source tree.
- Run every test and every post-deliverable verification gate inside Docker.
  No host-side `go test`. No host-side `bash -n`. No host-side `shellcheck`.
- Preserve every defect fix the 2026-05-09 backup rebuild added: FDB
  `backup_agent` sidecar, `Restorable: true` assertion, Yugabyte audit
  schema coverage, Temporal-DB coverage. See
  `incident_2026-05-09_seed_parallel_org/fdb_backup_report.md` and
  `incident_2026-05-09_seed_parallel_org/backup_rebuild_report.md`.

### Non-goals

- This plan does not touch the Phase 2 Wave 1 audit work. The audit
  consumer, dual recorder, and Kafka producer are out of scope. They keep
  their existing path under `cmd/audit-consumer/` and `internal/audit/`.
- This plan does not change the repair tooling under
  `internal/ops/repair_*.go` or `internal/ops/command_repair.go`. Those
  subcommands already live under `./server ops repair`.
- This plan does not redesign the SQL audit schema, the FDB key space, or
  the address index global-vs-scoped question. Those are tracked
  separately in `incident_2026-05-09_seed_parallel_org/retro_log.md`.
- This plan does not introduce new env-var driven config files (no TOML,
  no YAML, no JSON config). Config stays on `caarlos0/env` in
  `internal/config`.
- This plan stops short of writing the code. It is a written design.

## 2. Existing ops dispatch shape

The dispatcher already exists. The plan slots three new families into the
same registry without changing its shape.

### 2.1 Entry point in `cmd/server/main.go`

The relevant lines today are at
`cmd/server/main.go:63-93`. The switch on `os.Args[1]` covers
`migrate`, `seed`, `ops`, `audit-export`, `audit-verify`, and
`gen-audit-key`. The `ops` arm calls `runOps(cfg, os.Args[2:])` at
line 72.

`runOps` itself is small. It hands the slice of remaining args off to
`ops.RunCommand(ctx, cfg, args)` from `internal/ops/command.go:13`. Any
new subcommand we add lands inside that `RunCommand` switch, not inside
`main.go`.

### 2.2 Family dispatch in `internal/ops/command.go`

`RunCommand` at `internal/ops/command.go:13-44` is a flat switch over the
first arg. Today it handles `inspect`, `verify`, `validate`, `repair`,
`batch`, plus any registered batch-op name as a passthrough. The default
arm prints usage. Each family has its own `runXCommand` helper that
parses the next arg and dispatches further.

The deliverables in this plan add three new family arms:

- `case "backup":` to `runBackupCommand(ctx, cfg, args[1:])`
- `case "deploy":` to `runDeployCommand(ctx, cfg, args[1:])`
- `case "verify":` already exists at `internal/ops/command.go:30` for
  node-consistency verify. The existing `verify node` stays. The backup
  artifact verifier becomes `./server ops backup verify`, so the existing
  `verify` family is not overloaded.

### 2.3 Help and usage in `internal/ops/command_help.go`

`printOpsUsage` at `internal/ops/command_help.go:9-22` lists the families.
The plan adds two lines: `backup` and `deploy`. Each family gets its own
`printBackupUsage` and `printDeployUsage` near the existing
`printVerifyUsage` (`command_help.go:58`) and `printRepairUsage`
(`command_help.go:66`).

### 2.4 Registration pattern

Each ops file registers via `init()` and `Register(Operation{...})`. See
`internal/ops/repair_sequence_scope_ids.go:14-20`,
`internal/ops/backfill_addresses_preview.go:12-23`,
`internal/ops/reindex.go:12`,
`internal/ops/backfill_default_children.go:14`,
`internal/ops/repair_read.go:11`,
`internal/ops/repair_find.go:16`,
`internal/ops/repair_query.go:11`. Operation names are dot-separated
lowercase. The shared `Env` (`internal/ops/ops.go:49-62`) carries
`*config.Config`, `*pgxpool.Pool`, `*fdbadapter.Stores`, and a logger.

Backup, restore-test, and deploy do not need an FDB pool at the call
site. They orchestrate Docker. The plan adds a separate, lighter Env
constructor that opens nothing by default and lets each handler decide
what it needs. See section 3.5.

## 3. Deliverable 1: backup, verify, restore-test conversion

### 3.1 New CLI surface

```
./server ops backup                     run a full snapshot
./server ops backup verify <path>       structural inventory check
./server ops backup restore-test <path> end-to-end replay check
./server ops backup help                usage banner
```

Each subcommand maps to one Go entry-point function. None of them register
under the `Register(Operation{...})` legacy registry. They live under the
new `backup` family because their lifecycle is operator-driven, not
periodic-batch shaped (sequencing, prereqs, signal handling, exit codes
matter more than `Operation.Run`).

### 3.2 File layout under `internal/ops/`

The CLAUDE.md rule "no file over 200 lines" is binding. The plan splits
the work across small focused files. Each file states one concern in its
package doc comment.

```
internal/ops/backup.go                 family dispatcher and usage; ~80 lines
internal/ops/backup_run.go             top-level orchestration of `ops backup`; ~150 lines
internal/ops/backup_run_fdb.go         FDB step: sidecar lifecycle + fdbbackup; ~180 lines
internal/ops/backup_run_yugabyte.go    ysql_dump of public + audit; ~120 lines
internal/ops/backup_run_temporal.go    pg_dump of temporal; ~100 lines
internal/ops/backup_run_meili.go       Meilisearch named volume tar; ~80 lines
internal/ops/backup_manifest.go        sha256 + size + relpath manifest writer; ~100 lines
internal/ops/backup_verify.go          structural artifact inspection; ~180 lines
internal/ops/backup_restore_test.go    end-to-end replay test; ~200 lines (split if over)
internal/ops/backup_restore_fdb.go     FDB restore replay; ~180 lines
internal/ops/backup_restore_pg.go      Postgres-shaped artifact replay; ~120 lines
internal/ops/backup_restore_meili.go   Meilisearch replay; ~80 lines
internal/ops/backup_dockerctl.go       thin wrapper over the Docker SDK; ~150 lines
```

The naming follows the existing convention: the family stem `backup`
plus a verb. Compare `command_repair.go`, `command_help.go`,
`backfill_addresses_preview.go`, `repair_console_apply_test.go`. None of
the new files overlap with existing names.

The orchestration file `backup_run.go` calls into the four step files,
all of which take a `*backupCtx` value type that bundles run-id,
output dir, paths, image refs, and a `dockerClient`. No globals.

### 3.3 Mechanic preservation: what must not regress

These three defect fixes from the 2026-05-09 rebuild are non-negotiable:

- **FDB anonymous-volume shadowing.** The image declares
  `VOLUME /var/fdb/data` (production sweep findings, lines 113-117). A
  named-volume tar misses the actual data because Docker spawns an
  anonymous volume that shadows the mount. The Go code must use
  `fdbbackup` against a sidecar `backup_agent`, mirroring
  `scripts/backup-functions.sh:46-69` (`tack_backup_start_fdb_sidecar`,
  `tack_backup_run_fdb_start`).
- **Restorable assertion.** `fdbbackup describe` against the parent
  `file:///snapshot/<run-id>` returns `Restorable: false`. The describe
  URL must point at the timestamped `backup-*` subdirectory. The Go code
  ports the logic in `tack_backup_resolve_fdb_subdir`
  (`scripts/backup-functions.sh:91-102`) and the assert in
  `tack_backup_assert_restorable` (`scripts/backup-functions.sh:106-125`).
- **Yugabyte schema coverage.** The dump must include `--schema=public
  --schema=audit`, using `ysql_dump` from inside `tack-yugabyte-1`,
  matching `tack_backup_dump_yugabyte` at
  `scripts/backup-functions.sh:130-145`.
- **Temporal-DB coverage.** The Temporal database must be dumped from
  `tack-temporal-db-1` using `pg_dump`, matching
  `tack_backup_dump_temporal_db` at
  `scripts/backup-functions.sh:149-161`. Without this, in-flight workflow
  state has no recovery artifact.

Section 7.2 lists the rollback test that proves these fixes still hold
after the conversion.

### 3.4 Docker control: where does the binary run

This is the load-bearing question for deliverable 1. There are two
plausible architectures.

**Option A. Run on CT 117, talk to the local Docker socket.**

The operator runs `./server ops backup` either by `ssh tack /server ops
backup` against a binary baked into the production image, or by
`docker compose exec app /server ops backup`. The binary inside the
container talks to the host docker socket via a bind-mount of
`/var/run/docker.sock` into the app container. The ops handler uses
`github.com/docker/docker/client` to start a `backup_agent` sidecar,
exec into `tack-yugabyte-1` and `tack-temporal-db-1`, and
docker-run a one-shot alpine to tar the Meilisearch volume.

Trade-offs.

- Pro: zero new network surface. The CLI runs on the same host as the
  daemon. No DOCKER_HOST tunnel. SSH still works for human use.
- Pro: matches how the existing scripts run today (they execute on CT
  117 via `make backup` plus ssh).
- Pro: the binary already runs in the container, so its FDB cluster file
  mount and CA roots are already wired.
- Con: mounting `/var/run/docker.sock` into the app container weakens
  the security posture. A bug in the app container that lets an
  attacker write to the socket is escalation to host root. Today the
  app container does not have the docker socket.
- Con: the ops binary running inside the app container is the same
  process that serves HTTP. Either we run a second container with the
  same image and `--entrypoint /server`, or we do `docker compose exec
  app` and accept that ops shares the app's resource limits.

**Option B. Run on the operator's laptop, remote-control via
`DOCKER_HOST=ssh://tack`.**

The operator runs `./server ops backup` from the Mac. The binary uses
the Docker SDK with `DOCKER_HOST=ssh://tack` (or a configured
`docker context use tack`). All container lifecycle calls go over SSH
to the remote daemon. Local artifacts are streamed back via
`docker cp` (or by writing into a host bind-mount that the Mac then
rsyncs).

Trade-offs.

- Pro: no docker socket bind-mount in production. The app container
  stays unprivileged.
- Pro: the same binary works against any Docker context. The operator
  can point it at a local docker for dev testing, the QA host, or CT
  117 with the same flags.
- Con: SSH is now a hard dependency. The OpenSSH agent must be
  forwarded. Long-running operations (FDB restore replay can take
  minutes) hold the SSH session open.
- Con: pulling the artifact home means streaming it through SSH or
  bind-mounting through the SSH tunnel. Either way, slower than a
  local-only run.
- Con: requires a host-side `docker context` or a `DOCKER_HOST` env
  var. The plan must document this.

**Recommendation.** Option B. Run on the Mac. Use a `docker context`
named `tack` set to `ssh://tack`. The binary picks the context up via
the standard `DOCKER_HOST`/`DOCKER_CONTEXT` env vars that the Docker SDK
already honors.

Reasoning. The user has called a moratorium on rsyncing source. The same
moratorium logic applies to bind-mounting the docker socket: it adds a
production attack surface to solve a workflow problem. Option B keeps
production minimal and pushes complexity to the operator's laptop where
it belongs. The artifact retention path stays
`/root/backups/tack-<TS>/` on CT 117 (created by the remote daemon over
SSH), so existing offsite-pull workflows (`make backup-pull` in
`Makefile:171-177`) still work.

Verifiable claim. The Docker Go SDK
(`github.com/docker/docker/client`) reads `DOCKER_HOST`,
`DOCKER_CONTEXT`, and `DOCKER_TLS_VERIFY` automatically. The
`client.NewClientWithOpts(client.FromEnv)` constructor is documented in
the package godoc at pkg.go.dev/github.com/docker/docker/client. I have
not run a wet test of the `ssh://` scheme against CT 117 from this
environment, so the operator should expect a one-time round of
SSH-config tweaks (agent forwarding, `IdentitiesOnly`, jump host) before
this is reliable. Plan section 8 lists the open question.

Open mitigation. If Option B turns out to be too slow over the WAN to
CT 117 for restore-test (which spins up scratch FDB containers and
streams MB of data), section 8 lists the fallback: a thin Option-A
mode gated by an env var, where `./server ops backup --on-host` runs
inside the app container with a docker socket bind-mount. We do not
land both at once; we measure first.

### 3.5 Env shape change

Today `ops.NewEnv` (`internal/ops/ops.go:66-82`) opens a Postgres pool
and FDB stores unconditionally. The backup family does not need either:
the FDB step talks to the live cluster via fdbbackup, not via the Go
bindings, and the Yugabyte step talks to the YSQL endpoint via
`docker exec ysql_dump`, not via the app's own pool.

The plan adds a sibling constructor:

```go
// internal/ops/ops.go (new function added below NewEnv)
func NewMinimalEnv(ctx context.Context, cfg *config.Config) (*Env, error)
```

`NewMinimalEnv` returns an `Env` with `Pool` and `Stores` set to nil and
a usable logger. The backup, restore-test, and deploy handlers call
`NewMinimalEnv` instead of `NewEnv`. The existing `Run` function in
`internal/ops/ops.go:119-144` keeps using `NewEnv`. Each new family
handler builds its own env so there is no behavior change for the
legacy registered ops.

This is the smallest viable change. Adding a generic option-functional
constructor (`NewEnv(ctx, cfg, WithPool(), WithStores())`) is over-built
for a two-caller distinction.

### 3.6 Restore-test scratch container provisioning

The restore-test step is the most complex. It mirrors
`scripts/backup-restore-test.sh:231-580`, which uses Docker for FDB,
Postgres, and Meilisearch scratch containers. The Go version uses
`github.com/docker/docker/client` directly and orchestrates:

- One scratch FDB container per FDB artifact, with the same
  `FDB_NETWORKING_MODE=container`, `FDB_PORT=4500`, and
  `FDB_CLUSTER_FILE_CONTENTS=docker:docker@127.0.0.1:4500` env that the
  shell script uses (`scripts/backup-restore-test.sh:264-272`). The
  fdb.bash overlay at `fdb-overlay/fdb.bash` is bind-mounted in. The
  flow is identical to today: wait for the cluster file, configure new
  single memory, wait for healthy, run `fdbbackup describe`, start a
  `backup_agent` inside the container, run `fdbrestore start
  --waitfordone`, unlock, range-scan.
- One scratch Postgres container per .sql artifact (Yugabyte audit and
  Temporal-DB), using `postgres:16-alpine`. Stream the dump in via
  `docker exec -i ... psql -v ON_ERROR_STOP=1`.
- One scratch Meilisearch container, populated by an alpine one-shot
  that untars the Meili volume artifact into a fresh named volume,
  then started under `getmeili/meilisearch:v1.12`.

The Docker Go SDK is the right tool here. It is the SDK that the Docker
CLI itself wraps; every operation the shell script needs has a typed
counterpart: `ContainerCreate`, `ContainerStart`, `ContainerWait`,
`ContainerExecCreate`, `ContainerExecAttach`, `VolumeCreate`,
`VolumeRemove`, `ContainerLogs`. Reference: the package-level docs at
pkg.go.dev/github.com/docker/docker/client.

The shell script's `wait_for_exec` polling loop
(`scripts/backup-restore-test.sh:213-226`) becomes a small generic
`waitForExec(ctx, cli, container, timeout, cmd)` helper in
`backup_dockerctl.go`. The cleanup trap pattern
(`scripts/backup-restore-test.sh:120-167`) becomes `defer cleanup()`
plus a signal-aware context. Same semantics, much less surface area.

### 3.7 Verify (artifact structural check)

`./server ops backup verify <path>` mirrors
`scripts/backup-content-check.sh`. It does no Docker work. It reads files,
opens tar streams via Go's `archive/tar`, and pattern-matches header
names. Outputs a pass/fail line per artifact and a summary. The
2026-04-25 to 2026-05-09 silent-empty-backup defect class is exactly
what this catches: an archive with zero entries, or with entries that
have no FDB markers.

The detection table in `scripts/backup-content-check.sh:319-338` is
the source of truth for filename-to-category mapping. The Go version
ports it as a `categoryFromName` function with the same case set.

### 3.8 Verification gates for deliverable 1

These run inside the test container described in section 6.

- Unit tests cover the manifest writer, the artifact-category detector,
  and the restorable-line parser. Each tests a function without Docker.
- Integration tests cover one end-to-end backup run against a scratch
  compose stack (a stripped clone of `docker-compose.test.yml` plus a
  Meilisearch service). The test shells out to `./server ops backup`
  and asserts (a) `MANIFEST.txt` exists, (b) `fdb/describe.txt`
  contains `Restorable: true`, (c) every artifact's sha256 matches the
  manifest, (d) `./server ops backup verify <path>` exits 0, (e)
  `./server ops backup restore-test <path>` exits 0 against the
  scratch stack.
- Wet run on CT 117. The operator runs `./server ops backup` against
  production, then runs `./server ops backup verify` and `./server
  ops backup restore-test` against the resulting directory, all from
  the Mac with `DOCKER_CONTEXT=tack`. Acceptance is identical to the
  shell-based wet-run in
  `incident_2026-05-09_seed_parallel_org/backup_test_report.md`.

## 4. Deliverable 2: deploy

### 4.1 New CLI surface

```
./server ops deploy                     build, push (or stream), restart prod
./server ops deploy build               build only, no push
./server ops deploy push <ref>          push an already-built image
./server ops deploy restart             restart prod against the latest image
./server ops deploy help                usage banner
```

The default `./server ops deploy` covers the three steps in sequence,
with `--no-build`, `--no-push`, and `--no-restart` flags to skip any of
them. This matches the shape of `make deploy` today but lets the
operator stop at any step.

### 4.2 Image distribution: registry vs save/load

The current `make deploy` flow at `Makefile:123-135` rsyncs source and
runs `docker build` on CT 117. The new flow ships a built image. There
are two ways to do that.

**Option A. Registry.** Push the built image to a registry; the
production host pulls. Two registry choices in turn.

- A1. **GHCR private repo.** Free for the user account, integrates with
  GitHub auth, supports IPv6 client connections (verifiable by
  `dig AAAA ghcr.io`). Pull on CT 117 needs a one-time
  `docker login ghcr.io` with a personal access token that has
  `read:packages`. Push from the Mac uses the same token with
  `write:packages`.
- A2. **Self-hosted registry.** Stand up a `registry:2` container on
  the user's vault host or another always-on machine. Higher control,
  more moving parts. Reasonable if there is a need for an offline
  workflow but heavier than the problem warrants today.

**Option B. `docker save | ssh | docker load`.** Build locally, pipe
the tar over SSH, load on the remote.

```
docker save tack-server:<tag> | ssh tack docker load
```

Trade-offs.

- Pro: zero new infrastructure. No registry to maintain.
- Pro: works offline if a built tarball is on hand.
- Con: every deploy ships the full image (about 600-900 MB based on
  the current Dockerfile's runtime stage with FDB clients), not just
  the changed layers. Slow on a slow link.
- Con: no historical layer cache on the remote across deploys.
  Subsequent loads do not benefit from the prior load.

**Recommendation.** Option A1, GHCR, is the smallest new surface that
gives layer caching and a real artifact registry. Save/load is the
fallback for the offline case (section 8 open question on whether
offline deploy is actually a need).

Reasoning. The image is roughly 700 MB compressed. Over a residential
upload, save/load every deploy is annoying. Layer caching cuts steady
state to tens of MB. GHCR is free for private repos at the user's plan
tier (verifiable by checking docs.github.com/en/billing for the exact
plan; I have not confirmed Alex's plan, so this is one of the open
questions in section 8). Production registry credentials live in
`/etc/tack/ghcr-token` on CT 117 and are used by a one-time `docker
login` per host.

The deploy subcommand uses the Docker SDK to invoke
`ImageBuild`, `ImagePush`, and (on the remote, via the same DOCKER_HOST
mechanism in section 3.4) `ImagePull`, `ContainerStop`,
`ContainerRemove`, `ContainerCreate`, `ContainerStart`. Compose-up
behavior comes from running `docker compose up -d --no-build app` on
the remote, which the SDK does not cover directly. We invoke
`docker compose` via `os/exec` on the remote in that one place; that is
not a shell script, it is a single typed command call.

### 4.3 Image identity and version stamping

The Dockerfile already takes `COMMIT`, `BUILD_TIME`, `TAG`, `DIRTY` as
build args (`Dockerfile:19-22`). The deploy subcommand reads them from
`internal/version` plus `git rev-parse HEAD` and passes them to the
SDK's build call. The image tag is `tack-server:<commit-sha>` plus
`tack-server:latest`.

The compose file at `docker-compose.yml:3` references `tack-server:latest`
by default. After push, the deploy subcommand pulls
`tack-server:latest` on CT 117 (or whatever tag the operator chose) and
runs `docker compose up -d --no-build app audit-consumer`. The two
services that run the same image are picked up together so they stay in
lockstep.

### 4.4 Smallest production filesystem

After this deliverable lands and the cleanup in section 5 runs, the
production host's `/root` looks like:

```
/root/
  tack/
    docker-compose.yml             checked-in copy
    .env                           secrets (kept private)
    fdb-overlay/fdb.bash           IPv6-aware overlay, bind-mounted into fdb
  backups/                         created by ops backup runs
  fdb-snapshots/                   created by ops backup runs (intermediate)
```

Gone: `/root/tack/cmd`, `/root/tack/internal`, `/root/tack/migrations`,
`/root/tack/scripts`, `/root/tack/Dockerfile`, `/root/tack/go.mod`,
`/root/tack/go.sum`, every other source file. Section 5 covers the
order of operations for getting to this state safely.

Note. `docker-compose.yml` and `.env` stay on the host because compose
needs a file. The fdb-overlay stays because it is a runtime bind-mount
(`docker-compose.yml:126`). All three are tiny and tracked in the
repo. The deploy subcommand can rsync just those three paths if the
operator wants a one-step "compose file plus env" sync; that is
optional and behind a flag.

### 4.5 Verification gates for deliverable 2

- The image digest the deploy step pushed equals the digest the
  remote `docker pull` reports. The deploy command prints both and
  asserts they match.
- `./server ops deploy --no-restart` builds and pushes only; the
  operator can then run `./server ops deploy restart` on a separate
  invocation. This decoupling lets the operator confirm the image is
  on the remote before disrupting any service.
- Unit tests cover the version-stamp gathering and the image-tag
  builder.
- Integration test runs `./server ops deploy build --no-push` against
  a local Docker, then asserts the image carries the right
  `org.opencontainers.image.revision` label and the right
  `internal/version` ldflags. Both verifiable by `docker inspect` or
  `docker run --rm <image> /server --version` (a flag we may need to
  add; today the version is logged at startup).

## 5. Deliverable 3: migration and cleanup

### 5.1 Order of operations

The new tooling lands in this order. Each step has a clear rollback
window.

**Step 1. Land `./server ops backup verify`.** No production behavior
change. The verify subcommand only reads files. Operators run it
in parallel with the existing shell-based check. If they disagree,
investigate before deleting either.

**Step 2. Land `./server ops backup`.** Add the new code. Do not
delete `scripts/backup.sh` or `scripts/backup-functions.sh` yet.
Operators run both in parallel for at least one cycle (one daily
backup window). If `MANIFEST.txt` from each path produces the same
sha256 list (modulo timestamps), the new path is a drop-in.

**Step 3. Land `./server ops backup restore-test`.** The systemd
unit at `scripts/systemd/tack-backup-restore-test.service:13`
currently shells out to `scripts/backup-restore-test.sh`. Edit the
service file to call `docker compose exec app /server ops backup
restore-test "/root/backups/tack-${ts}"` instead. Same timer, same
schedule (`scripts/systemd/tack-backup-restore-test.timer:6`).

**Step 4. Land `./server ops deploy`.** Use it for one deploy in
parallel with `make deploy` (or behind a feature flag). Once the
remote shows the new image and the smoke checks (HTTP /healthz,
MCP describe, audit ledger writes) pass, `make deploy` becomes the
fallback.

**Step 5. Delete the shell scripts.** After two clean cycles of each:
- Delete `scripts/backup.sh`.
- Delete `scripts/backup-functions.sh`.
- Delete `scripts/backup-content-check.sh`.
- Delete `scripts/backup-restore-test.sh`.
- Update `Makefile`: remove the `deploy`, `backup`, `backup-content-check`,
  and `backup-restore-test` targets. Replace each with a thin shim that
  calls `./server ops <thing>`. The `make deploy-pull` and
  `host-maintenance-install` targets stay because they cover host-side
  systemd, which is not in scope for this conversion.

**Step 6. Wipe production source tree.** After steps 1-5, the
production host no longer needs `/root/tack/cmd`, `/root/tack/internal`,
etc. Remove them. Keep `/root/tack/docker-compose.yml`,
`/root/tack/.env`, `/root/tack/fdb-overlay/`. Document this in
`docs/phase2-wave1-runbook.md` (already exists in working tree at
`?? docs/phase2-wave1-runbook.md` per `git status`).

### 5.2 Systemd timer wiring

The current unit (`scripts/systemd/tack-backup-restore-test.service:13`)
runs:

```
ExecStart=/bin/bash -c 'set -euo pipefail; ts=$(cat /root/backups/.latest); /root/tack/scripts/backup-restore-test.sh "/root/backups/tack-$ts"'
```

After conversion, the service becomes:

```
ExecStart=/usr/bin/docker compose -f /root/tack/docker-compose.yml exec -T app /server ops backup restore-test "/root/backups/$(cat /root/backups/.latest)"
```

The `-T` disables TTY allocation so systemd captures stderr cleanly.
The compose project name is whatever `tack` resolves to on CT 117
(default: parent dir name, which is `tack`).

Two wrinkles to call out.

- The `app` service runs the HTTP server. `docker compose exec` runs
  alongside the server's main process; it does not replace it. The
  server tolerates this because the binary's main switch in
  `cmd/server/main.go:63-93` exits cleanly after the ops command
  finishes (return statements at lines 67, 70, 73, 76, 79, 91).
- The restore-test step itself starts scratch containers via the
  Docker SDK. From inside the app container we need access to the
  host docker socket. This is the opposite trade-off from section 3.4:
  for the timer-driven path, we have no operator at the keyboard and
  no SSH session, so an in-container exec with a socket bind-mount is
  the only ergonomic option.

The plan resolves this by adding a single bind-mount to the `app`
service in `docker-compose.yml`:

```yaml
volumes:
  - /var/run/docker.sock:/var/run/docker.sock
```

This is a security trade-off. The app container is already trusted (it
holds the audit signing key, the Yugabyte writer DSN, and the FDB
cluster file). Adding the docker socket gives it equivalent host
control. It is not free. The plan recommends doing it because the
timer-driven path needs it and the alternative (a separate
`tack-ops` Compose service) doubles the image footprint. Section 8
flags this for the operator to confirm.

If the operator rejects the socket bind-mount on the app, the
fallback is a sibling Compose service `tack-ops` that mounts the
socket and is brought up only for ops runs:

```yaml
services:
  tack-ops:
    image: tack-server:latest
    profiles: ["ops"]
    entrypoint: ["/server", "ops"]
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /root/backups:/root/backups
```

Same image, separate container, ops-profile gated so it does not run
by default. The systemd unit exec line becomes
`docker compose --profile ops run --rm tack-ops backup restore-test ...`.

### 5.3 QA validation strategy

The 2026-05-09 retro added a hard rule: no migration, seed, backfill,
or restore touches prod without QA validation against real-shape data.
This conversion is exactly that class of change.

**Recommendation.** Stand up a QA env before this lands. Two reasons.

- The conversion is large enough that an end-to-end test against the
  `tack-20260509T232955Z` snapshot does not cover everything.
  Snapshot-driven tests cover backup correctness and restore-test
  correctness; they do not cover the deploy path or the systemd timer
  rewiring.
- The deploy step is the highest-risk change in this plan. A
  push-then-restart rollout against an image that has a regression
  is a production incident in the time it takes the readiness probe
  to fail. QA gives us a place to land that regression instead.

If the operator is not ready to stand up QA before this work, the
fallback is to land deliverables 1 and 3 first (backup, verify,
restore-test, systemd timer rewiring) and to defer deliverable 2
(deploy) until QA exists. Backup and restore-test are read-only and
near-zero risk. Deploy is the production-changing one.

### 5.4 Specific cleanup sequence

The exact filesystem actions, ordered:

1. Merge the new code under `internal/ops/backup_*.go` and the
   `case "backup":` arm in `internal/ops/command.go`. CI build
   passes inside Docker (section 6). Land in `main`.
2. Run `./server ops backup` once on CT 117. Compare
   `MANIFEST.txt` to the prior run's. Run
   `./server ops backup verify` and `./server ops backup
   restore-test`. All green.
3. Delete `scripts/backup.sh` and `scripts/backup-functions.sh`.
   Remove `make backup` and `make backup-content-check` and
   `make backup-restore-test` targets in `Makefile`.
4. Edit the systemd unit (under
   `scripts/systemd/tack-backup-restore-test.service`) to call
   the binary. Reload systemd and verify the next timer fire
   succeeds: `systemctl status tack-backup-restore-test.service`.
5. Land deploy. Use it for one production deploy. Compare image
   digest pre and post. Smoke-test the deployed app.
6. Delete the `make deploy` and `make deploy-preflight` targets
   in `Makefile`. Replace with shims that call `./server ops
   deploy`.
7. On CT 117: `rm -rf /root/tack/cmd /root/tack/internal
   /root/tack/migrations /root/tack/proto /root/tack/gen
   /root/tack/scripts /root/tack/Dockerfile* /root/tack/go.mod
   /root/tack/go.sum /root/tack/Makefile /root/tack/buf.* `.
   Keep `/root/tack/docker-compose.yml`, `/root/tack/.env`,
   `/root/tack/fdb-overlay/`.

## 6. Docker-only testing strategy

The user constraint is that all tests and verification gates run inside
Docker. This applies to unit tests, integration tests, and the
post-deliverable acceptance gates. Below is the full plan; the existing
test scaffolding makes most of it cheap.

### 6.1 Unit tests

Today, `make test` runs `go test ./...` on the host. The new contract
runs the same test set inside `Dockerfile.test`'s image, against a
bind-mount of the working tree.

Concrete change: add a `test-unit` make target.

```make
.PHONY: test-unit
test-unit:
	docker compose -f docker-compose.test.yml --profile runner build tests
	docker compose -f docker-compose.test.yml --profile runner run --rm \
	    --no-deps \
	    tests \
	    /usr/local/go/bin/go test -count=1 ./...
```

`--no-deps` skips the FDB and Yugabyte service dependency that is only
needed for integration tests. The bind-mount of `.` to `/src` is
already in `docker-compose.test.yml:74-75`. The image is already in
`Dockerfile.test:1-44`.

This replaces every host-side `go test` invocation. Documentation in
`AGENTS.md` (which CLAUDE.md symlinks to per the user-instructed
"do not run `go build` from a non-module-root directory" rule) gets
updated to refer operators to `make test-unit` instead of any
host-side `go ...`.

### 6.2 Integration tests

The existing path at `Makefile:62-64` already runs integration tests
inside Docker via `scripts/test-integration.sh` and
`docker-compose.test.yml`. The plan reuses it.

For backup integration tests specifically, we need a stack with FDB
plus Yugabyte plus Temporal-DB plus Meilisearch. `docker-compose.test.yml`
today only ships FDB and Yugabyte. The plan adds two services
(temporal-db, meilisearch) under the same `runner` profile so they
come up only when the integration test asks for them. They reuse the
production image refs (`postgres:16-alpine`, `getmeili/meilisearch:v1.12`)
so the test stack matches production.

The integration test harness then exercises backup and restore-test
against this scratch stack, end to end, with no production
involvement.

### 6.3 The host-side prohibition is a soft floor

There is one place where host-side commands are unavoidable: the
command that invokes Docker. `docker` is a host binary. So is
`docker compose`. The plan accepts this and treats "host-side" to
mean "host-side Go compilation and host-side `go test`." Invoking
`docker compose run --rm tests go test ./...` is host-side, but the
test runtime is fully containerized.

### 6.4 Verification gate commands per deliverable

Each gate is one operator command. All run on the operator's Mac.

**Deliverable 1, backup, verify, restore-test:**

```
make test-unit                                              # unit tests in docker
make test-integration                                       # integration in docker
DOCKER_CONTEXT=tack ./bin/server ops backup                 # wet run on CT 117
DOCKER_CONTEXT=tack ./bin/server ops backup verify /root/backups/tack-<TS>
DOCKER_CONTEXT=tack ./bin/server ops backup restore-test /root/backups/tack-<TS>
```

Acceptance:
- `make test-unit` exits 0.
- `make test-integration` exits 0.
- `ops backup` produces `MANIFEST.txt` and `fdb/describe.txt` with
  `Restorable: true`.
- `ops backup verify` exits 0.
- `ops backup restore-test` exits 0.

**Deliverable 2, deploy:**

```
make test-unit
DOCKER_CONTEXT=tack ./bin/server ops deploy --no-restart
DOCKER_CONTEXT=tack ./bin/server ops deploy restart
DOCKER_CONTEXT=tack docker compose -f /root/tack/docker-compose.yml ps
curl -s https://<prod-host>/healthz                         # if healthz exists; else /mcp probe
```

Acceptance:
- `ops deploy --no-restart` reports the same digest pre and post push.
- `ops deploy restart` reports the new container coming up healthy.
- The deployed binary's `--version` (or the startup log line at
  `cmd/server/main.go:57-61`) shows the expected commit and tag.

**Deliverable 3, cleanup:**

```
ssh tack 'ls /root/tack'                                    # only docker-compose.yml, .env, fdb-overlay/
ssh tack 'systemctl status tack-backup-restore-test.timer'  # active
ssh tack 'systemctl status tack-backup-restore-test.service' # inactive (last exit success) or never run
```

Acceptance:
- The production tree contains only the three expected entries.
- The systemd timer is active.
- The service unit, when last run, exited 0.

## 7. Risks and rollback

### 7.1 The risk that motivated this plan

The 2026-04-25 to 2026-05-09 production window shipped empty FDB
backups every day for two weeks without anyone noticing. The defect
was a named-volume tar of a path the FDB image had shadowed with an
anonymous volume. The fix lived in shell scripts. Replacing those
scripts with Go code carries the obvious risk: the new code could
have its own silent-failure mode that nobody catches until the next
recovery is needed.

Mitigations.

- The `restorable` assertion is a hard fail, not a warning. The Go
  port preserves the same `grep -q '^Restorable: true'` semantics by
  parsing the describe output and erroring if the line is absent or
  set to `false`.
- The `verify` subcommand runs immediately after every backup. The
  operator workflow is `ops backup && ops backup verify`, not
  `ops backup` alone. The Makefile shim and any docs make this the
  default.
- The systemd timer continues to run `restore-test` daily. It is the
  end-to-end check that catches the class of defect that `verify`
  alone misses (a structurally valid archive that fdbrestore refuses
  to replay).
- Test coverage on the manifest writer, the describe parser, and the
  category detector is inside Docker, not host-side. The integration
  test runs once per CI build, not just before a release.

### 7.2 Specific rollback maneuvers

If `./server ops backup` regresses:

1. Re-add the deleted `scripts/backup.sh` and
   `scripts/backup-functions.sh` from git history. Their last good
   commit is the one that fixed the Yugabyte ysql_dump rename
   (`1934828` per current git log).
2. Restore the `make backup` Makefile target.
3. Use shell-based backup until the regression is rooted out.

If `./server ops deploy` ships a bad image:

1. SSH to CT 117. Run `docker compose pull tack-server:<previous-tag>`
   then `docker compose up -d --no-build app`. The previous-tag is
   whatever the operator captured before the failed deploy ran (the
   deploy command logs both the previous and the new digest).
2. The previous image is still in the local registry on CT 117 because
   `docker compose down` is never invoked by the deploy path. The
   `docker images` cache holds the prior tag.

If the systemd timer regression hits:

1. Edit the unit back to call `scripts/backup-restore-test.sh`. The
   original ExecStart line is in this plan at section 5.2.
2. `systemctl daemon-reload && systemctl restart
   tack-backup-restore-test.timer`.

### 7.3 Risks specific to the deploy registry choice

If GHCR is the chosen registry and Alex's account hits the free-tier
storage cap, pushes will fail until older images are pruned. The
deploy subcommand should prune old `tack-server:*` tags after each
successful deploy; the prune retention defaults to "keep the last 5"
and is configurable via env var (`TACK_DEPLOY_REGISTRY_KEEP=5`).

If the Mac has no IPv4 path and GHCR returns AAAA only intermittently,
push can hang. The fallback is `--via-save-load` flag, which streams
the image to CT 117 over SSH and skips the registry entirely.

### 7.4 Risk: the docker socket bind-mount

Section 5.2 documents the trade-off. The mitigation list:

- Run the bind-mount only on the `tack-ops` profile service, not on
  `app`, if the operator wants to keep `app` socket-free.
- Audit-log every ops invocation. The ops binary already writes to
  `audit.events` for state-changing operations; backup and
  restore-test count as such.
- The FDB cluster file mount (`/etc/foundationdb`) is read-only in
  `docker-compose.yml:43`. The new socket mount is read-write by
  necessity. This is the only read-write host bind-mount the app
  needs.

## 8. Open questions

These are the answers I cannot infer and need from the operator before
implementation starts.

1. **Docker remote-control mechanism.** Confirm Option B (laptop
   `DOCKER_CONTEXT=tack`, ssh transport) is acceptable. The
   alternative is running ops inside the app container with a host
   socket bind-mount. I lean B; you decide.
2. **Image registry.** Is GHCR private acceptable, given Alex's
   GitHub plan? If yes, what tag scheme? `tack-server:<commit>` plus
   `tack-server:latest`? If GHCR is not preferred, is a self-hosted
   `registry:2` already running somewhere accessible from CT 117?
3. **Save/load fallback.** Should `--via-save-load` ship in the first
   cut, or wait until we have evidence the registry path is
   unreliable? I lean wait.
4. **Socket bind-mount on `app`.** Section 5.2 trade-off. Keep the
   socket out of `app` and use a separate `tack-ops` Compose
   service? Or accept the bind-mount and keep image count at 1? My
   default is the separate service.
5. **QA environment.** Stand up QA before any of this lands? Or
   land deliverables 1 and 3 against snapshot-driven tests first
   and defer deliverable 2 until QA exists? Today's incident report
   makes the first option the safer call. Confirm.
6. **`./server --version` flag.** The deploy path's smoke test wants a
   version flag that prints commit, tag, build time, dirty without
   starting the server. Today the same info is in the startup log at
   `cmd/server/main.go:57-61`. Adding a `--version` flag is a small
   addition under `cmd/server/main.go`. Yes or no?
7. **Audit logging for ops invocations.** Backup and restore-test are
   state-relevant in a different sense from regular CRUD. Should the
   ops binary record an `audit.events` entry per run? If yes, what
   actor? `ops:<operator-email>` from `git config user.email`?
8. **Compose project name on CT 117.** The systemd exec line in
   section 5.2 assumes `docker compose -f /root/tack/docker-compose.yml`
   without `-p`. Confirm this matches today's deploy convention. If
   the project name is set explicitly anywhere, the new ExecStart
   needs to match.
9. **fdb-overlay placement.** The plan keeps
   `/root/tack/fdb-overlay/fdb.bash` as a runtime bind-mount
   (`docker-compose.yml:126`). If we eliminate the source tree under
   `/root/tack/`, we still need the overlay file there. Either keep
   `fdb-overlay/` under `/root/tack/` or move it into a
   purpose-named host path like `/etc/tack/fdb-overlay/` and update
   the compose file. Either is fine; pick.
10. **Pre-existing `verify` family.** The current
    `internal/ops/command.go:30` `verify` arm dispatches
    `verify node`. The new `backup verify` is a separate sub-command
    under `backup`, not `verify`. Confirm this is the intent and not
    a structural conflict the operator wants to resolve differently
    (for example, by promoting `verify backup <path>` instead of
    `backup verify <path>`).
