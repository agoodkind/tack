# Tack incident response handoff

**Drafted:** 2026-05-09 evening, after a long Saturday of incident response.
**Updated:** 2026-05-10 after a session pause and resume; the session name is `tack-incident-and-finish-migration`.
**From:** Claude session (Opus 4.7, 1M context). The session paused at `/exit` and resumed under the same name; the surviving transcript is the source of truth for events that happened before the resume.
**For:** The next agent reading this cold, whether continuing this session or starting fresh.

This document is meant to be self-contained, but the operator has reminded me that it is incomplete in places, so treat it as shaky ground truth rather than gospel. Every term and concept is defined inline. The next agent should not need any prior session context to act on what is written here.

The session-continuity status as of 2026-05-10 is that the next queued action is the TACK-228 lint pass described in section 13. No agents are currently in flight. The deploy subcommand work in main checkout is in the same shape as section 12 describes, the backup tooling worktree is in the same shape as section 7.2 describes, production is still on commit `23ad44a` with Phase 1 deployed, and nothing has been pushed to the remote.

---

## 0. Preamble: how today started, what came after, and the order to read this in

This handoff describes the state of work after a long Saturday of incident response on the Tack platform. The state is layered, because problems were discovered in sequence rather than all at once, and because each discovery changed what the next piece of work needed to be. The next agent reading this document will find references to "Phase 1", "Phase 2 wave 1", a half-built backup tool, a stopped deploy agent, and roughly fifteen Tack tickets, and none of those will make sense without the order in which they came into existence. This preamble lays out that order so the rest of the document reads cleanly.

### 0.1 The original incident had nothing to do with audit

The conversation began because the operator was in the middle of executing a plan called "finish-slug-and-state", and the plan blew up on production. The plan covered two separate in-flight migrations that needed to land together. The first migration was a refactor of how Tack stores human-readable references in FoundationDB, where an older key family called `slug_index` was being generalized into a more capable family called `address_index`. The refactor's code had already merged on 2026-05-06 in commit `584176e1`, but the data backfill that should have copied existing `slug_index` rows into the new `address_index` had not yet been run against production. The second migration was a state-repair stream, which was the cleanup pass for a 2026-05-08 audit that had found 562 issues and epics with stale workflow-state alias data. Some of that repair had been applied to production already, and some was still pending policy decisions.

The operator started the rollout by deploying the slug-ready code to production and running `seed` so the seed metadata would update to reflect the new reference contracts. The seed function checks whether the org and workspace exist before creating them, and it does that check against the `address_index` family because that is the new system. The legacy production data was still in `slug_index`, because the backfill had not run yet. Seed therefore concluded that the `goodkind-io` org and the `main` workspace did not exist, generated fresh deterministic UUIDs for them under the new derivation rules, and wrote those new UUIDs into `address_index` and `node_resolve`. From that moment on, MCP requests for `workspace_reference: "main"` resolved to the new empty workspace, while the real production projects, issues, and epics continued to live under the legacy UUIDs in a part of FDB that nothing was reading anymore. The platform looked dead from a user's perspective even though every byte of data was still on disk.

Forward-fix recovery rather than backup-restore was the recovery path, because the operator discovered during recovery planning that the production FDB backups had been silently empty for two weeks. That discovery is the second story of the day, and it leads directly into Phase 1.

### 0.2 Phase 1 was a downstream discovery, not the original problem

The forward-fix did its job, and the parallel-org outage was resolved in the early afternoon. Two new findings emerged from the recovery work itself. The first finding was the empty-backup defect: the backup script had been writing tar archives of a Docker named volume that the FoundationDB container image was shadowing with an anonymous volume, so for two weeks every backup had been a few kilobytes of metadata and zero bytes of actual database content. The second finding was that the audit subsystem had a quiet bug of its own. While inspecting the audit ledger to confirm what had and had not been recorded during the outage, the operator and the agent found that no read-class audit events had landed in the database for more than eleven hours. The bug turned out to be in the audit write-ahead log: a stuck idle segment had tripped an overflow check that was supposed to protect against drainer backups, and the check then dropped every subsequent read event silently because the dropper code path treated overflow as a soft failure.

This bug is what "Phase 1" refers to throughout the document. Phase 1 is the fix for the audit-WAL silent-drop bug, and it shipped today at commit `23ad44a`. Phase 1 had nothing to do with the original slug-and-state migration that opened the day. It was a separately serious bug that the recovery process happened to surface, because the operator was looking carefully at compliance state. The two threads share a calendar day, but they are independent problems with independent fixes.

### 0.3 Phase 2 was a design conversation that started after Phase 1 shipped

Once Phase 1 was in production and the audit drops had stopped, the conversation moved to whether the audit subsystem's architecture should be redesigned more deeply, because the WAL approach has structural limits and because the operator wants horizontal scale-out from day zero. That design conversation produced two long planning documents at `audit_horizontal_design.md` and `audit_two_phase_plan.md`, and it ultimately landed on Apache Kafka as the destination for an audit-on-Kafka migration. Phase 2 is the name for that migration. The migration is risky enough to need staged deployment, so it is broken into "waves" rather than a single cutover.

Wave 1 is the dual-write phase, where the producer writes every audit event to both the existing Yugabyte WAL system and a new Kafka topic, and a separate audit-consumer service reads from Kafka and projects into a sibling Yugabyte table called `audit.events_v2`, so the operator can compare the two paths during a parity window before letting wave 2 swap them. Wave 2 is the cutover, where Kafka becomes primary and Yugabyte becomes the derived projection, with `audit.events_v2` renamed in place to take the canonical name. Waves 3 and 4 are scale-out and cold archive, respectively, and are still planning-only. The wave 1 code committed today at `a5aec6d`. The dual-write semantics were never explicitly approved as a separate decision, because they were baked into the design document by the planning agent, so the operator's explicit approval applies only to "Apache Kafka as the destination" rather than to "dual-write as the wave 1 shape". If the operator does not want dual-write specifically, the wave 1 commit needs revisiting before TACK-231 can deploy.

### 0.4 The TaskList items that did not become Tack tickets

The session used two different tracking systems in parallel. Tack tickets (numbered TACK-227 and onward through TACK-242, plus the five new epics TACK-12 through TACK-16) are the durable, persistent record that survives this conversation and lives in Tack itself. The TaskList items (numbered roughly #41 through #46) are session-scoped, because they were either trivial cleanup that did not warrant a ticket, planning work whose output is the deliverable rather than a ticket, or implementation work that produced enough code to warrant filing implementation tickets but whose own task entry was just the in-session marker.

TaskList #41 (clean up orphan `phase2-wip` branch) is a trivial git cleanup that takes one command once you confirm the branch has no unique content versus main. It did not warrant a ticket because the work has no design content and no deploy implication. TaskList #42 (revise wave 1 runbook) completed in-session and produced the runbook revision committed at `1a06138`, so the ticket would have been closed before it was even filed. TaskList #43 (build Go backup tooling) produced the implementation that lives in the `tack-backup-restore-test` worktree. The only piece of remaining work there is the lint pass, which is filed as TACK-228, because the implementation itself does not need a separate ticket once the worktree IS the deliverable. TaskList #44 (build deploy subcommand) produced the partially-built deploy work that was stopped before the SDK-based implementation was fully complete, and the remaining work is filed as TACK-233 because it is now design-and-implementation rather than just implementation. TaskList #45 (plan the wave 2 cutover) produced the planning document, originally `wave2_cutover_plan.md`, since rewritten on 2026-05-10 as `audit_db_to_kafka_cutover.md`. The eventual deploy of wave 2 is filed as TACK-232. TaskList #46 (decide address-index design) produced the decision document `address_index_design_decision.md`, but the operator pushed back on the document's recommended approach, and the cleaner architectural answer was filed as TACK-229 plus TACK-230 instead.

The pattern is that planning and implementation tasks within a session are tracked in TaskList because they need finer-grained progress markers than a Tack ticket would carry. The durable outcomes (deploys, ongoing maintenance, blocking decisions) get filed as Tack tickets so they survive the session.

---

## 1. What Tack is

Tack is a project management platform built by a single operator (`alex@goodkind.io`) as a competitor to Plane CE, Linear, and Jira. The product is multi-tenant by design from day zero. Today there is one production tenant, with org slug `goodkind-io` and workspace slug `main`, hosting seven projects: TACK, MWAN, APP, CLYDE, OSS, WEBSITE, and LAB.

The codebase lives at `/Users/agoodkind/Sites/tack`. It is a Go monorepo. The single binary at `cmd/server/main.go` is the entire server: HTTP, MCP, Connect-RPC, ops tooling, migrations, seed.

The platform is composed of these services:

- **Server binary** (`./server`). Runs the HTTP listener at port 8000. Serves MCP at `/mcp`. Serves expvar metrics at `/debug/vars`. Has subcommands `migrate`, `seed`, and `ops`.
- **FoundationDB (FDB).** Distributed key-value store. Holds all product data: orgs, workspaces, projects, issues, epics, states, labels, and custom types. Every product entity is a node in FDB.
- **YugabyteDB.** PostgreSQL-compatible distributed SQL. Holds only auth and audit. Tables are `users`, `api_tokens`, `org_members`, plus the `audit.*` schema (`audit.events`, `audit.chain_heads`, `audit.notarizations`, `audit.pii`).
- **Meilisearch.** Full-text search index. Populated on every Tack entity write.
- **Temporal.** Workflow engine for background jobs. Has its own PostgreSQL backing store called `tack-temporal-db-1`.
- **Apache Kafka.** Phase 2 wave 1 component. Code committed but not deployed. Will become the audit ledger transport.
- **Apache ClickHouse.** Phase 2 wave 1 planned component. OLAP read path for audit queries.
- **SeaweedFS.** Phase 2 wave 4 planned component. Cold archive for audit.

The production deployment is a single LXC container called CT 117 at IPv6 address `3d06:bad:b01::117`, SSH alias `tack`. Every service runs in that one container via `docker compose`.

---

## 2. Architecture vocabulary

These terms appear throughout the documentation and the tickets. Each is defined once, and the rest of the document uses them without redefining.

- **Node.** Any entity in Tack: org, workspace, project, issue, epic, state, label, or custom type. The same storage pattern applies across all of them. Every node has a UUID, an `orgID`, a `nodeType` string, properties, and relationships. Nodes live in FDB.
- **`orgID`.** The UUID identifying the tenant that owns a node. Multi-tenancy is enforced by including `orgID` early in every FDB key tuple, so two tenants' data sit under different prefixes and cannot collide at the storage layer.
- **`NodeValue`.** The primary FDB record for a node, keyed `(orgID, workspaceID, nodeType, nodeID)`.
- **`NodeListView`.** A materialized read view of a node, keyed similarly. Holds everything needed to render a list row, so reads do not need follow-up lookups.
- **`NodeResolve`.** A global FDB record keyed by `nodeID` alone, mapping to `(orgID, workspaceID, projectID, nodeType)`. This is how the system resolves any node UUID without prior org context.
- **Property.** A piece of data on a node. Examples include a node's `name`, `description`, `priority`, `slug`, or `identifier`. Properties are typed and indexed in `node_by_property`.
- **`node_by_property`.** The FDB index that maps `(orgID, workspaceID, nodeType, propDefID, encodedValue)` to `nodeID`. The index is at `internal/adapters/foundationdb/keys.go:124-127`. This is the org-scoped property index.
- **`address_index`.** A second FDB index that maps `(nodeType, addressKind, address)` to `nodeID`. Global, no orgID. Lives at `internal/adapters/foundationdb/keys.go:140-142`. The 2026-05-09 incident's root cause was that this index is global and duplicates data already in `node_by_property`. TACK-229 deletes the family entirely by routing address lookups through the org-scoped `node_by_property` index instead. No backfill is needed because the address data is already on the underlying nodes as properties.
- **Address.** Tack's term for a human-readable identifier that resolves to a node UUID. Examples include a slug like `main`, an identifier like `TACK`, or an issue reference like `TACK-227`.
- **Slug.** One specific shape of address. Lowercase, hyphenated. Examples are `goodkind-io` and `main`. Stored as a property on the relevant node.
- **MCP.** Model Context Protocol. The streamable-HTTP API at `/mcp`. The primary interface to Tack. Roughly fifty tools like `tack_list_workspaces`, `tack_get_project`, and `tack_list_issues`. Authenticated via `Authorization: Bearer <token>`.
- **WAL.** Write-ahead log. Tack's audit subsystem uses a local-disk WAL at `/var/lib/tack/audit-wal/` for read-class audit events. The WAL is written before the event reaches YugabyteDB.
- **Read-class audit verb.** An audit event for a read operation. Examples are `auth.token_used`, `node.read`, `node.list`, `workspace.list`, `audit.read`, and `node.search`. These go through the WAL path.
- **State-change audit verb.** An audit event for a write operation. Examples are `node.create`, `node.update`, and `node.delete`. These bypass the WAL and write synchronously to YugabyteDB so the user's request returns only after the audit record is durable.
- **Producer.** The part of the audit subsystem that creates an audit event when something happens. Lives inside the main `tack-app-1` server process; it is not a separate service.
- **Consumer.** The standalone Go binary at `cmd/audit-consumer/` that runs as its own Compose service. Reads audit events from Kafka and projects them into YugabyteDB. The consumer exists because Kafka by itself only stores the event in a log, so somebody has to read it back out and put it into a queryable database.
- **Writer.** The role that actually does the database insert into the audit table. Today the producer is the writer because the producer writes synchronously to YugabyteDB. After Phase 2 wave 2 (the hard cutover), the consumer becomes the writer because the producer hands the event off to Kafka and the consumer is the only thing that puts rows into `audit.events`.
- **Verb.** The name of the action that an audit event records, named for the grammatical role of subject (the user) doing verb (the action) to object (the entity). The full enum lives at `internal/audit/verbs.go`.
- **Dual-write.** The Phase 2 wave 1 design where the audit producer writes every event to two destinations at once: the existing YugabyteDB path (current source of truth) and a new Apache Kafka topic. A separate consumer projects the Kafka stream into a sibling table. Dual-write exists so the operator can compare the two paths during a parity window before letting wave 2 swap them.
- **Hard cutover.** The Phase 2 wave 2 approach the operator approved on 2026-05-09. The earlier plan was a gradual cutover with parity gates and rollback windows; the operator dropped that in favor of a hard cutover, because Tack has no external clients yet, so a brief audit-availability gap during the rename is acceptable. The hard cutover renames `audit.events_v2` to `audit.events` and `audit.events_v2_dlq` to `audit.events_dlq` in a single migration, then flips the producer to Kafka-only mode, then validates with a smoke test.
- **DLQ.** Dead-letter queue. A holding area where a consumer writes messages it cannot process normally (malformed payload, schema mismatch, downstream unavailable) instead of crashing or silently dropping them. Tack's audit DLQ is `audit.events_v2_dlq`, which the wave 2 migration renames to `audit.events_dlq`. The consumer's DLQ-write logic lives at `internal/audit/consumer.go:765-778`.
- **Soak.** Operations term for letting a system run under normal load for a watching-it-bake period to surface latent problems before declaring the change done. Phase 2 wave 1's plan called for a 24-hour parity soak before wave 2; with hard cutover replacing gradual, the soak is still a recommended confidence-builder but is no longer a blocker.
- **Epic.** A grouping of related Tack tickets under a long-running theme. Each ticket has exactly one parent epic. Today's session created five epics: TACK-12 (the 2026-05-09 incident itself, historical reference), TACK-13 (audit subsystem migration to Apache Kafka), TACK-14 (multi-tenancy and node ID refactor), TACK-15 (ops tooling consolidation), and TACK-16 (operational foundations).
- **expvar.** Go's standard-library metrics package. Metrics are exposed as JSON at `/debug/vars`. Tack uses expvar everywhere; TACK-227 will migrate to OpenTelemetry, replacing the paired-int histogram emulation and colon-joined-string label synthesis with the native shapes OpenTelemetry provides.
- **agent-gate.** A pre-execution hook on the operator's machine that blocks dangerous patterns: em-dashes in prose, `//nolint` directives, baseline tampering, and direct `go` calls outside `make`. The hook cannot be bypassed.

---

## 3. The 2026-05-09 incident in two paragraphs

The session started with a Phase 2 wave 1 deploy attempt. The deploy ran a `seed` pass against production. Seed creates the deterministic org and workspace nodes if they do not exist, and it checks via the new `address_index` family. The legacy production data was indexed under the predecessor `slug_index` family, not the new one. Seed could not see the legacy rows, decided the org and workspace did not exist, and created parallel UUIDs for `goodkind-io` and `main`. Both sets of UUIDs co-existed in different parts of FDB. MCP began returning the new empty workspace for `workspace_reference: "main"` while the real data still existed under the legacy UUIDs.

Recovery was forward-fix, not backup-restore, because production FDB backups had been silently empty since 2026-04-25. The backup script was tarring a Docker named volume that was shadowed by an anonymous volume due to the FDB image's `VOLUME /var/fdb/data` directive. The forward-fix rewrote three stale `direct_slug` NodeType records to `direct_property`, deleted the 37 keys belonging to the parallel new org, repointed the two conflicting `address_index` rows to the legacy UUIDs, deleted one stale `org_members` SQL row, and ran `backfill.addresses.apply` to populate the new index from legacy `slug_index` data. After recovery, an additional bug surfaced: the audit-WAL had been silently dropping all read-class events for over eleven hours due to an idle-segment-not-rotating bug. That was the Phase 1 fix.

---

## 4. What got built today (in order)

Each item names what it is, why, where the code lives, and current ship status.

### 4.1 Phase 1 audit-WAL fix
**What.** Three changes to `internal/audit/wal.go`. The drainer now force-rotates an idle active segment after 500ms. The filename-age overflow check is replaced with atomic backlog counters (`unflushedSegments`, `oldestUnflushedAgeSecs`, `lastDrainSuccessUnix`). ENOSPC now propagates rather than silently dropping.
**Why.** The bug had been silently dropping read audit events for over eleven hours. The trigger was a stuck idle filename.
**Where.** Committed at `23ad44a` on the `phase2-wave1-rebase` branch. Deployed to production on 2026-05-09 around 22:32 UTC.
**Status.** Live in production. Verified end-to-end: four MCP calls produced eight audit rows in `audit.events` with matching verbs.

### 4.2 Backup script rebuild (shell)
**What.** `scripts/backup.sh` was rewritten using a `tack-backup-agent` sidecar pattern that runs `fdbbackup` properly. Yugabyte backup now uses `ysql_dump` (not `pg_dump`, which does not exist in the Yugabyte image). Temporal-DB was added to coverage via `pg_dump`. The script writes a SHA-256 manifest. A new helper file at `scripts/backup-functions.sh` holds the shared logic.
**Why.** Backups had been silently empty for two weeks. The FDB image's `VOLUME` directive caused a Docker anonymous volume to shadow the named `tack_fdb-data` mount, so the old script tarred the named volume and got nothing useful.
**Where.** Committed across `b0fc2a0`, `a1a25be`, `d2da217`, and `1934828`.
**Status.** Working in production. The last good backup is at `/root/backups/tack-20260509T232955Z/`. Sizes were FDB 1.0 MB, Yugabyte 1.1 GB, Temporal-DB 278 KB, Meilisearch 13 MB. Running `make backup` produces a valid backup. SHA-256 matches between local and remote.
**Caveat.** This is shell, and the operator has put a moratorium on shell scripts. The Go replacement is TACK-228 (the mechanical lint-fix pass that unblocks `make build` on the new Go backup tooling) plus the partial implementation work in the `tack-backup-restore-test` worktree.

### 4.3 Phase 2 wave 1 audit dual-write code
**What.** A new audit producer that fans every event to both the existing YugabyteDB path and a new Apache Kafka topic. A new `audit-consumer` binary that reads from Kafka and projects into a sibling table at `audit.events_v2`. Three new migrations create `audit.consumer_offsets`, `events_event_id_uniq`, and `audit.events_v2` plus `audit.events_v2_dlq`. Phase 2 wave 1 monitoring contributed nine metrics and four slog events. The audit parity verification subcommand `./server ops audit parity` was added.
**Why.** Phase 2 of the audit refactor. Wave 1 is dual-write, so wave 2 can cut over to Kafka-primary with confidence. The architecture is shaped for horizontal scale-out.
**Where.** Committed at `a5aec6d` on `phase2-wave1-rebase`: 27 files, 4,198 insertions, 21 deletions. Files include `cmd/audit-consumer/`, `internal/audit/{consumer,dual,kafka_recorder,clock,monitoring_test}.go`, `internal/ops/audit_parity*.go`, `migrations/003-005`, and Compose service additions for `kafka`, `seaweedfs`, `clickhouse`, and `audit-consumer`.
**Status.** Code committed; not yet deployed to production. The build is clean, audit package tests pass in 42 seconds, and lint is clean.
**Operator runbook.** `docs/phase2-wave1-runbook.md`, revised at commit `1a06138` to match real verb names, log lines, metric names, and to remove references to a banned shell parity script.

### 4.4 Worktree cleanup
**What.** Reduced 19 or more worktrees from prior slug-to-address refactor work down to three active worktrees. Phase 1 of cleanup deleted eleven safe ones; phase 2 deleted five more after content-against-main diffing.
**Why.** Cruft. Most worktrees held superseded designs, and a few held experimental work that never landed.
**Status.** Done. Three worktrees remain (see section 7). One orphan branch named `phase2-wip` is flagged in TaskList #41.

### 4.5 Backup tooling Go subcommand (partial)
**What.** Three Go subcommands at `./server ops backup`, `./server ops backup verify <path>`, and `./server ops backup restore-test <path>`. These replace the four shell scripts. The binary runs Mac-side and uses `DOCKER_CONTEXT=tack` over SSH. The implementation is twelve new files under `internal/ops/backup_*.go` plus a `dockerctl.go` helper.
**Why.** The shell-script moratorium plus the four production defects today (FDB anonymous-volume, missing backup_agent sidecar, Temporal-DB exclusion, ysql_dump binary name) needed durable Go-typed fixes.
**Where.** In worktree `/Users/agoodkind/Sites/tack-backup-restore-test` on branch `backup-restore-test-9d4f7a` (which is at `dd430c9`, two commits behind main). All untracked files.
**Status.** Code compiles, tests pass, gofmt is clean. `make build` FAILS on roughly 60 staticcheck-extra and golangci-lint findings (mostly mechanical). The agent declined to suppress lint or extend baselines, because `make baseline` is operator-gated. TACK-228 tracks the mechanical fix.

### 4.6 Deploy subcommand (partial; agent stopped before completing)
**What.** `./server ops deploy` subcommand. An image-based deploy that builds the application image locally on the operator's Mac via the Docker Go SDK, saves it to a tar stream, transports it over SSH to the production host using `DOCKER_CONTEXT=tack`, loads it on the remote via the same SDK, and then runs `docker compose up -d` on the remote. There is no container registry anywhere in the flow, because Tack does not run one. This replaces the existing `make deploy` rsync-source pattern.
**Where.** In MAIN checkout `/Users/agoodkind/Sites/tack`, NOT in an isolation worktree (the agent had a path-bug like several prior agents and wrote to absolute paths in main). Files added: 11 untracked `internal/ops/deploy*.go` files plus the shared `internal/ops/dockerctl.go`. Files modified: `go.mod`, `go.sum`, `internal/config/config.go`, `internal/ops/command.go`, `internal/ops/command_help.go`.
**Status.** Incomplete. The Docker integration is written against the Docker Go SDK at `github.com/docker/docker/client`, and the operator has confirmed that the SDK is the correct, idiomatic, and not-up-for-litigation approach for Tack. Five of the deploy files use the SDK and are exactly the shape we want to keep. The single open question is the `govulncheck` failure that currently blocks `make build`, because daemon-side CVEs ride along in the SDK's transitive dependency tree. The next agent must investigate and resolve the `govulncheck` failure without abandoning the SDK; section 12 documents the concrete paths to investigate.

### 4.7 Seed hard-exit guard
**What.** A short guard added at the top of `runSeed` in `cmd/server/seed.go:47` that unconditionally calls `slog.Error` and `os.Exit(1)` with a message naming TACK-230 and the 2026-05-09 incident. The guard makes any invocation of `./server seed` fail immediately, regardless of environment.
**Why.** The 2026-05-09 incident root cause was a seed pass running against already-bootstrapped production. Until TACK-230 lands and replaces the slug-hashing OrgID derivation with random UUIDv7 (and folds in a proper seed-against-non-empty-database refusal), seed cannot run anywhere safely. The hard-exit is the simplest blocker that prevents accidental re-occurrence.
**Where.** Uncommitted in main checkout at `cmd/server/seed.go:47`. The original config-missing check stays underneath the guard so that removing the guard restores the original behavior cleanly.
**Status.** In place locally, not yet committed. The guard is a temporary measure that TACK-230 deletes when it ships. No production deploy is needed for the guard itself, because seed is operator-invoked rather than auto-run.

---

## 5. Operator-imposed rules (do not violate)

The operator established these rules during this session. Treat them as binding.

### 5.1 No new shell scripts
No new `.sh` files. The existing scripts are slated for replacement under TACK-228 (the lint pass that unblocks the Go backup tooling that replaces `scripts/backup.sh`, `scripts/backup-functions.sh`, `scripts/backup-content-check.sh`, and `scripts/backup-restore-test.sh`) plus TACK-234 (the final cleanup that wipes `/root/tack` source tree on production and rewires the systemd backup-restore-test timer to invoke the Go binary instead of bash). The shell scripts will not be extended. If you find a bug in an existing shell script, do not commit a fix to git; the script is dying. Hand-rsync the fix to production once if absolutely necessary, then file or update a Go-replacement ticket.

### 5.2 No hand-rsync to production
Anything that ends up on the production host (CT 117 at SSH alias `tack`) must come from a real deploy. The current `make deploy` is grandfathered until TACK-233 lands. The replacement ships an image-based deploy as `./server ops deploy`, which builds the image locally with the Docker Go SDK, saves it to a tar stream, sends it to the remote over SSH using `DOCKER_CONTEXT=tack`, loads it on the remote with the same SDK, and runs `docker compose up -d` against the loaded image. There is no container registry in the flow because Tack does not operate one. After TACK-233 lands, the only sanctioned path to production is `./server ops deploy`.

### 5.3 Plain English over jargon
The operator pushes back hard on jargon. As one example from this session, "the gate-of-64 mechanism doesn't backpressure under low concurrency, which is exactly Tack's production shape" had to be rephrased as "single producer never blocks because the gate has 64 slots free, so a stuck drainer doesn't get caught". Use everyday words. Define jargon when it is introduced.

### 5.4 Verify-don't-trust agent outputs
Subagent reports describe intent, not outcome. Always verify load-bearing claims against primary sources (the actual file, the actual test run, the actual log line) before forwarding. The session had specific instances where one agent was wrong: an "audit table doesn't exist" claim was based on querying the wrong database, and the table did exist.

### 5.5 Real sentences with subjects and verbs
The `agent-gate` hook blocks em-dashes mechanically, but the rule is broader. Avoid bare double-hyphens, en-dashes, and fused thoughts (two ideas glued together with a dash to avoid finishing either). Each idea gets its own sentence with its own subject and its own reason to exist. The em-dash check is a proxy; the rule is sentence structure.

### 5.6 No nolint directives, no baseline tampering
If a lint finding fires, fix the code or restructure it. Do not add `//nolint`. Do not modify `staticcheck-extra` baselines without running `make lint-baselines`, which is operator-gated. The `agent-gate` hook blocks attempts at both.

### 5.7 No QA-skipping
Migrations, seed, backfill, and restore must validate against QA-real-shape data before touching production. TACK-235 is the QA env standup (separate host, separate network, identical Compose stack to production, real-shape data with PII redacted, documented refresh procedure, clearly labeled at every layer so it cannot be mistaken for prod). Until QA exists, the operator is skeptical of every "works on my machine" claim.

### 5.8 Don't punt cleanup back to the operator
When agents produce work that needs sorting (worktrees, stale branches, partial files), the calling agent sorts it out. Do not push the sorting back to the operator. Make decisions; the operator can override.

### 5.9 Don't ship things that depend on broken backup
The operator clarified P0: do not SHIP anything that depends on backup until the new backup tooling is solid. P0 does NOT mean stop all parallel work. Build and plan on parallel tracks; gate only the actual deploy.

---

## 6. Production deployment topology

- **Host.** CT 117. LXC container at IPv6 address `3d06:bad:b01::117`. SSH alias `tack` is configured in the operator's `~/.ssh/config` with ProxyJump through `vault`. IPv6-only with NAT64 gateway for external IPv4 reach.
- **Container runtime.** Docker (NOT Podman; the operator unaliased `docker` from `command podman` earlier in the session at `~/.dotfiles/zshrc/commands/prefer-decls.zsh`).
- **Compose project name.** `tack`. Containers are `tack-app-1`, `tack-fdb-1`, `tack-yugabyte-1`, `tack-meilisearch-1`, `tack-temporal-1`, `tack-temporal-ui-1`, and `tack-temporal-db-1`.
- **Network.** `tack_default`, IPv6-only with GUA out of `3d06:bad:b01:0:7ac::/96`. NDP-proxied onto `eth0`. Inter-container DNS uses AAAA records via Docker's embedded resolver.
- **Source tree on remote.** Currently at `/root/tack`, populated by full source rsync from `make deploy`. TACK-234 wipes this in favor of just `docker-compose.yml` and `.env`, and the same ticket rewires the systemd backup-restore-test timer to invoke the Go binary instead of bash, then deletes the four backup shell scripts after the Go replacement is proven.
- **Backup directory on remote.** `/root/backups/tack-<UTC-timestamp>/`. Files are organized as per-component subdirectories (`fdb/`, `yugabyte/`, `temporal-db/`, `meilisearch/`) plus a `MANIFEST.txt` with SHA-256, size, and relpath per file. The pointer at `/root/backups/.latest` holds the most recent timestamp.
- **FDB snapshot directory.** `/root/fdb-snapshots/snapshot-<UTC-timestamp>/`. Holds the raw `fdbbackup` artifact tree before it gets tarred into the per-run backup directory.
- **Docker context.** The operator's Mac has `docker context tack` configured to point at `ssh://root@tack`. The Go ops tooling reads `DOCKER_CONTEXT=tack` and uses that for remote operations.

---

## 7. Worktree topology

`git -C /Users/agoodkind/Sites/tack worktree list` returns three worktrees today.

### 7.1 Main checkout
- **Path.** `/Users/agoodkind/Sites/tack`.
- **Branch.** `phase2-wave1-rebase`.
- **HEAD.** `1a06138` ("Revise wave 1 runbook with real verb names, log lines, and parity command").
- **Recent commits (newest first).** `1a06138` runbook revision, `a5aec6d` Phase 2 wave 1 audit code, `1934828` ysql_dump fix, `d2da217` FDB cluster file env var, `a1a25be` remove `-C` flag, `b0fc2a0` backup system rebuild, `23ad44a` Phase 1 WAL fix.
- **Uncommitted state.** Tracked-modified: `go.mod`, `go.sum`, `internal/config/config.go`, `internal/ops/command.go`, `internal/ops/command_help.go`, `scripts/backup-content-check.sh`, plus a few files inside the `docs/incidents/2026-05-09-seed-parallel-org/` directory that were just edited as part of the 2026-05-10 reorganization. Untracked: 11 `internal/ops/deploy*.go` files plus `internal/ops/dockerctl.go` (the deploy agent's partial work in main checkout, written against the Docker Go SDK as the operator has mandated, with the only open issue being the `govulncheck` failure during `make build` that section 12 documents in detail), and the new tracked-but-not-yet-committed `docs/incidents/2026-05-09-seed-parallel-org/` tree (safe to commit; production tokens, raw audit data, and snapshot tarballs were removed during the cleanup).
- **Notes.** The `scripts/backup-content-check.sh` modification is a SIGPIPE-fix on a dying script. The operator said do not commit. The deploy agent's files are unbuildable in their current state.

### 7.2 Backup tooling worktree
- **Path.** `/Users/agoodkind/Sites/tack-backup-restore-test`.
- **Branch.** `backup-restore-test-9d4f7a`.
- **HEAD.** `dd430c9` ("Add generic address backfill operations"). Two commits behind current main.
- **Uncommitted state.** Tracked-modified: `Makefile`, `go.mod`, `go.sum`, `internal/config/config.go`, `internal/ops/command.go`, `internal/ops/command_help.go`. Untracked: 12 new `internal/ops/backup_*.go` files (the actual subcommand code), `internal/ops/backup_dockerctl.go`, `.github/workflows/backup-content-check.yml`, `docs/`, `docs/incidents/2026-05-09-seed-parallel-org/`, and `scripts/backup-content-check.sh`.
- **Status of work.** Code compiles, `go test ./internal/ops/...` passes, gofmt is clean. `make build` fails on about 60 lint findings; the fix is TACK-228 (the mechanical pairing pass that adds `slog.ErrorContext` calls before each `fmt.Errorf` return and routes `time.Now` through a `clock.go` file). After that lint pass lands, this branch needs to rebase onto main (`1a06138`).
- **Final report from the building agent.** At `docs/incidents/2026-05-09-seed-parallel-org/backup_tooling_implementation_report.md` IN THIS WORKTREE (also visible from main as an untracked file).

### 7.3 Phase 2 wave 1 wip worktree
- **Path.** `/Users/agoodkind/Sites/tack-phase2-wave1-wip`.
- **Branch.** `phase2-wave1-wip`.
- **HEAD.** `dd430c9`.
- **State.** Mostly idle. Originally held wave 1 audit code while it was being staged. The work has since been committed to main at `a5aec6d`. Files here are superseded.
- **Recommendation.** Probably safe to delete after verifying nothing unique remains. Defer until after backup tooling and deploy work settles.

### 7.4 Orphan branch `phase2-wip`
There is one local branch in the main checkout that has no worktree backing it: `phase2-wip`. The worktree-cleanup pass during today's session found it while consolidating worktrees down from 19 to 3. The branch turned out to be a staging branch from earlier in the slug-to-address refactor that never landed anything unique. The cleanup agent declined to delete it autonomously because it had not run the full content-against-main diff. The next agent should run `git -C /Users/agoodkind/Sites/tack diff phase2-wip main` to confirm there is no unique content. If the diff is empty or contains only superseded shapes, then `git -C /Users/agoodkind/Sites/tack branch -D phase2-wip` finishes the cleanup. This is hygiene work and does not warrant a Tack issue; fold it into the cleanup-and-stabilize pass that follows the deploy subcommand work.

---

## 8. Tack issues, organized by epic

Every ticket filed during this session has a parent epic in Tack. The epics are themselves Tack issues with a special type. The next agent can call `tack_get_epic` or `tack_get_issue` against any reference to see the live description. Below is the structure as of this handoff. Each ticket appears under exactly one epic, and the dependency order within each epic reflects what should land first.

### 8.0 TACK-12: Incident 2026-05-09 (parallel-org outage)
This epic is a historical reference rather than an active workstream. The incident response itself was completed during the session: the forward-fix recovery for the parallel org, Phase 1 (the audit-WAL idle-rotation fix shipped at commit `23ad44a`), and the shell-based backup script rebuild. The improvements that came out of the incident live in their own domain epics below, because each one outlives the incident and belongs to a long-running theme.

The doc set for this epic is `docs/incidents/2026-05-09-seed-parallel-org/retro_log.md` plus this handoff. The epic closes when the retro is signed off; no open tickets are filed under it.

### 8.1 TACK-13: Audit subsystem migration to Apache Kafka
This epic covers the long-running effort to move Tack's audit subsystem from a Yugabyte-write-ahead-log shape onto Apache Kafka, with YugabyteDB as the projection target. The migration is deliberately staged so each step can be validated and rolled back before the next one ships. The tickets below are listed in the order they need to land.

- **TACK-228** is filed under TACK-15 (ops tooling), but it gates this epic because TACK-231 cannot ship until `make build` passes cleanly. See section 8.3 for the description.
- **TACK-231** Phase 2 wave 1 production deploy. **Priority high.** Deploy the audit dual-write code at commit `a5aec6d` to CT 117. Phase 2 wave 1 is the dual-write phase, where the audit producer fans every event to both the existing Yugabyte path and a new Apache Kafka topic, and a separate `audit-consumer` binary projects the Kafka stream into a sibling table at `audit.events_v2`. Wave 1 was originally planned to run a 24-hour parity soak before wave 2; the soak is still recommended even though wave 2 is now a hard cutover. The operator runbook is `docs/phase2-wave1-runbook.md`. This blocks on TACK-228 going green and on operator confidence in the new Go backup tooling.
- **TACK-232** Phase 2 wave 2 hard cutover. **Priority medium.** The operator approved a hard cutover with rename and smoke tests on 2026-05-09; the gradual approach was dropped because Tack has no external clients yet, so a brief audit-availability gap during the rename is acceptable. The wave 2 migration renames `audit.events_v2` to `audit.events` (the legacy table is archived as `audit.events_legacy_archive`), renames `audit.events_v2_dlq` to `audit.events_dlq`, flips the producer to Kafka-only mode, and confirms with a five-call smoke test. The authoritative cutover plan is at `docs/incidents/2026-05-09-seed-parallel-org/audit_db_to_kafka_cutover.md` (rewritten 2026-05-10 from the earlier `wave2_cutover_plan.md`). Blocks on TACK-231 deploying cleanly.
- **TACK-227** Migrate metrics from `expvar` to OpenTelemetry. **Priority medium.** Replace the standard-library `expvar` package (where today's metrics live and are exposed at `/debug/vars` as JSON) with OpenTelemetry meters across `internal/telemetry/metrics.go` and the new wave 1 monitoring code. Today's metrics emulate histograms with paired ints and synthesize labels as colon-joined strings, which OpenTelemetry models natively. Sequenced after the QA environment stands up (TACK-235) and before Phase 2 wave 4 (the eventual cold-archive deploy).
- **TACK-236** Extend backup content-check coverage for the new audit tables. **Priority low.** Today's backup verify hardcodes a four-table list. After wave 1 deploys, the verify must also cover `audit.events_v2` and `audit.consumer_offsets`. After wave 2 swaps, the table list shifts again. Make the list configurable so it follows schema changes without code edits. Blocks on TACK-228 because the shell script being patched is the one TACK-228 is replacing; do not fix a dying script.
- **TACK-240** Build audit DLQ replay tool with exponential backoff. **Priority low.** Today the DLQ table exists and the consumer writes to it correctly, but no tool exists to replay events out of the DLQ back through the consumer's normal path. This ticket builds `./server ops audit dlq replay` and `./server ops audit dlq inspect` with an exponential-backoff retry budget. Not needed for cutover; needed once Tack has external clients producing real audit-event volume.

### 8.2 TACK-14: Multi-tenancy and node ID refactor
This epic fixes the architectural drift that the 2026-05-09 incident exposed in Tack's tenant-isolation and node-identity layers. Two tenants with the same slug currently collide in the FDB key prefix because of how OrgIDs are derived. A parallel global address-index family duplicates data already in the property index. The codebase has hardcoded type-specific ID-derivation functions that violate the "everything is a node in FDB" architecture. Tickets are listed in dependency order.

- **TACK-241** Enforce slug normalization rules at the domain boundary. **Priority high.** Today there is no slug validation anywhere in `internal/domain`, `internal/service`, or the MCP tools. The codebase accepts whatever string the caller passes, including capitalization variants, whitespace, and unicode lookalikes. Add a single `ValidateSlug` function in `internal/domain/node/slug.go` that enforces lowercase ASCII letters, digits, and hyphens only, with no leading or trailing hyphen, no consecutive hyphens, max length 64, with a typed `domain.ErrInvalidSlug` for rejections. Validate at every entry point.
- **TACK-230** Fix OrgID derivation to use random UUIDv7. **Priority high.** Today `OrgID(slug)` at `internal/domain/node/types.go:288-290` hashes the slug with SHA-1, so two customers picking the same slug (`acme`) get byte-identical UUIDs and collide in the FDB key prefix. The operator-approved fix is to drop deterministic derivation in favor of random UUIDv7 (the timestamp-prefixed random format the rest of Tack already uses for new entities). An explicit override path preserves the seed-and-test workflow. The seed production guard is folded into this ticket; today the seed function has an unconditional hard-exit at `cmd/server/seed.go:47` that this ticket replaces with the proper guard.
- **TACK-229** Remove the redundant `address_index` FDB family. **Priority high.** Today there are two FDB lookup tables for human-readable references: `node_by_property` (org-scoped at `keys.go:124-127`) and `address_index` (global at `keys.go:140-142`). The two duplicate the same data because addresses are stored as properties on the underlying nodes anyway. Delete `address_index`, route every reader through `node_by_property` with the org context from auth, and delete the existing keys. No backfill is needed because the data is already in the property index. This depends on TACK-230 landing first or in lockstep, otherwise the org-scoped lookup still has tenant collisions because OrgIDs collide upstream.
- **TACK-242** Replace hardcoded type-specific node ID derivation with a generic NodeID function. **Priority high.** Today `internal/domain/node/types.go` has three hardcoded type-specific functions (`OrgID`, `WorkspaceID`, and `SystemPropID`) that bake specific node types into the language layer. The "everything is a node in FDB" architecture says the type list is data, not code, so these functions are a leak of the architecture into the compiler. Replace them with one generic function `NodeID(parentScope, address)` that works the same way for every node type, regardless of whether the type is `org`, `workspace`, `project`, or some custom type a future tenant defines. TACK-230 should land first because it removes the most acute multi-tenancy bug; this ticket consolidates the pattern across the codebase afterward.

### 8.3 TACK-15: Ops tooling consolidation
This epic moves every ops-shaped tool from shell scripts to Go subcommands under `./server ops *` and replaces the rsync-source-tree deploy with an image-based deploy. The 2026-05-09 incident showed that the existing shell scripts had multiple silent bugs (SIGPIPE under pipefail, missing `-C` flag on `fdbbackup`, `pg_dump` versus `ysql_dump` binary mismatch) and that hand-rsyncing files to production is itself a class of bug. The operator declared a moratorium on new shell scripts and on hand-rsync. This epic is the rebuild that satisfies both rules.

- **TACK-228** Pair `slog.Error` with `fmt.Errorf` in the new Go backup tooling. **Priority high (P0).** This is a mechanical lint-fix pass over twelve backup files in the `tack-backup-restore-test` worktree: roughly sixty findings total (about thirty unlogged `fmt.Errorf` returns missing paired `slog.ErrorContext` calls, twenty-four `slog.Error` calls missing `err` fields, two bare `time.Now()` calls that need to route through a `clock.go` file, and eight miscellaneous gosec or godoclint or wrapcheck items). It blocks `make build`, which blocks every downstream ticket that depends on the backup tooling. **This is the next thing the next agent should do.**
- **TACK-233** Replace `make deploy` with `./server ops deploy`. **Priority medium.** The new flow uses the Docker Go SDK at `github.com/docker/docker/client` to build the image locally on the operator's Mac, save it to a tar stream, transport the stream over SSH using `DOCKER_CONTEXT=tack`, load it on the remote with the same SDK, and run `docker compose up -d`. There is no container registry because Tack does not run one. The Docker Go SDK is the not-up-for-litigation choice for Docker control. Eleven `internal/ops/deploy*.go` files plus a shared `internal/ops/dockerctl.go` already exist in the main checkout (uncommitted), but `make build` does not yet pass because of a `govulncheck` failure in the SDK's transitive dependency tree. The next agent must resolve `govulncheck` without abandoning the SDK. Section 12 documents the four investigation paths.
- **TACK-239** Run `backup_agent` as a persistent Compose service. **Priority medium.** The current backup flow brings up a `tack-backup-agent` sidecar at the start of every backup run and tears it down at the end. Move it to a persistent Compose service with `restart: unless-stopped`, so failures restart on a known schedule and per-run startup latency goes away. Schedule after the Go backup tooling lands so the conversion happens once.
- **TACK-234** Wipe `/root/tack` source tree and finish ops cleanup. **Priority medium.** End state: the production host holds only `docker-compose.yml` and `.env`, the four backup shell scripts are deleted, the systemd timer for `backup-restore-test` invokes the Go binary instead of bash, and `make deploy` plus `make backup` are deleted from the Makefile. Blocks on TACK-228, TACK-233, and end-to-end QA validation under TACK-235.

### 8.4 TACK-16: Operational foundations
This epic covers the operating-environment work that the 2026-05-09 incident exposed as missing. None of these tickets are about a specific feature; they are about the platform around the product. Each one represents something the operator did not have on 2026-05-09 and which made the incident harder than it needed to be.

- **TACK-235** Stand up an out-of-band QA environment. **Priority high.** Separate host, separate network, identical Compose stack to production, real-shape data with PII redacted, documented refresh procedure, clearly labeled at every layer so it cannot be mistaken for prod. Recommended gate before TACK-229, TACK-230, TACK-233, and TACK-234 ship.
- **TACK-237** Clean up stale `api_tokens` rows. **Priority low.** Production has five `api_tokens` rows for the same user, surfaced during the 2026-05-09 read-only sweep. Hygiene work, not a security blocker.
- **TACK-238** Design and ship an incident-mode runtime control plane. **Priority low.** A hot-reloadable mechanism that lets the operator throttle or block specific verbs, orgs, shards, or node types during an incident without a deploy. The 2026-05-09 retro section 1E captured the finding; recovery would have been faster if the operator could have flipped runtime knobs without restarting the app.

### 8.5 Pre-existing tickets
TACK-1 through TACK-11 plus TACK-17 through TACK-226 predate this session. TACK-205, TACK-209, TACK-212, and TACK-215 came up during incident recovery as candidate Done states. The operator has not acted on them this session, and the next agent should treat their state as advisory rather than authoritative.

---

## 9. Files produced this session

All paths below are under `/Users/agoodkind/Sites/tack/docs/incidents/2026-05-09-seed-parallel-org/` unless noted otherwise. The directory was reorganized on 2026-05-10: stale plans (a rejected refactor proposal, an off-target research pass, a Phase 1 verification checklist that was no longer needed, transient revision notes, post-completion status files) were deleted, and sensitive artifacts (production token in WAL binaries, raw production state files, FDB and audit snapshot tarballs totaling about 83 MB) were removed because production backups now serve the same purpose. The remaining markdown was relocated from a sibling directory at the repo root into `docs/incidents/` so the incident lives alongside other tracked documentation, with individual agent run reports collected in a `reports/` subdirectory. The directory is now safe to commit; it contains no production tokens or raw audit data.

### 9.1 Plans and decisions (top-level of the incident dir)

- **`ops_consolidation_plan.md`** (936 lines). Designs the move of backup, verify, restore-test, and deploy under `./server ops`. Three deliverables in total. The plan recommends a Mac-side binary that talks to the production host using `DOCKER_CONTEXT=tack` over SSH, and it requires that all tests run inside Docker. The original plan document mentions a container registry as the deploy transport, but that recommendation has since been overridden by the operator: Tack does not operate a registry, so the deploy uses `docker save` piped over SSH to `docker load` instead. Implementation has started but is incomplete; Deliverable 1 is partial in the `tack-backup-restore-test` worktree, and Deliverable 2 is partial in the main checkout.
- **`audit_horizontal_design.md`** (1,439 lines). Phase 2 horizontal-from-day-one architecture. Apache Kafka producer, ClickHouse OLAP read path, Yugabyte integrity tables, Iceberg cold archive on SeaweedFS or Garage. Five waves from N=1 to N=many.
- **`audit_two_phase_plan.md`.** Phase 1 (WAL fix, shipped) and Phase 2 (Kafka migration) plan.
- **`audit_scale_architectures.md`** (839 lines). Research on real systems at multi-million EPS. Conclusion: no public system delivers hash chain plus signing plus PII plus 1M+ EPS together. Sigstore Rekor is the closest analogue at about 2 to 3k EPS per shard.
- **`audit_db_to_kafka_cutover.md`.** The wave 2 cutover plan, originally drafted as `wave2_cutover_plan.md` (1,004 lines, gradual approach) and rewritten on 2026-05-10 to match the operator-approved hard-cutover decision. The renamed document is now the authoritative wave 2 plan; the old document was deleted as part of the rewrite.
- **`address_index_design_decision.md`** (455 lines). The agent landed on Option C hybrid (org slug global, everything else scoped to orgID). The operator's correct framing is to delete the family entirely under TACK-229, removing the redundant `address_index` FDB family at `keys.go:140-142` and routing address lookups through `node_by_property` (which is already org-scoped at `keys.go:124-127`). No backfill is needed because the address values are already stored as properties on the underlying nodes. Read this report for context but do not act on its Option C recommendation.
- **`remediation_playbook.md`.** The read-only investigation that produced the exact mutations the forward-fix executed. Includes pre-conditions, post-conditions, and reversibility per step. Historical evidence for how the recovery happened.
- **`retro_log.md`.** The live retrospective. Sections 1A through 1H cover the empty-backup defect, the address-index drift, missing QA env, the missing-audit-table claim (later proven wrong), the incident-mode runtime control plane idea, the SIGPIPE bug, the shell-script moratorium, and the ops consolidation plan launch.
- **`post_incident_roadmap.md`.** Tier 1 through 4 follow-up roadmap. Partially superseded by Tack issues filed since.
- **`HANDOFF.md`.** This file.

### 9.2 Reports (under `reports/` subdirectory)

These are individual agent run reports, kept as audit trail rather than as forward-looking documents. The next agent should not need to read these to act on TACK-228 or the rest of the work; they are evidence of what happened during the session.

- **`backup_rebuild_report.md`** and **`backup_test_report.md`.** Backup script rebuild and validation.
- **`fdb_backup_report.md`.** The 2026-05-09 manual `fdbbackup` snapshot proof. Documents the proven sidecar mechanics.
- **`audit_snapshot_report.md`.** Pre-Phase-1-deploy audit table snapshot evidence. (The 71 MB tarball it described was deleted as part of the 2026-05-10 cleanup; the report itself is preserved as audit trail.)
- **`audit_table_investigation.md`.** The "audit table doesn't exist" claim was wrong; this documents the investigation that proved that.
- **`production_sweep_findings.md`.** Production read-only sweep that surfaced the FDB anonymous-volume defect and the Temporal-DB backup gap.
- **`execution_report.md`** and **`state_repair_execution_report.md`.** Forward-fix and state-repair execution logs.
- **`phase1_compliance_fix_report.md`.** Phase 1 implementation report.
- **`phase2_wave1_rebase_report.md`** and **`phase2_wave1_runbook_report.md`.** Phase 2 wave 1 rebase and runbook agent reports.
- **`wave1_producer_implementation_report.md`**, **`wave1_consumer_implementation_report.md`**, **`wave1_monitoring_implementation_report.md`**, **`wave1_runbook_verification_report.md`**, **`wave1_runbook_revision_report.md`.** Phase 2 wave 1 build, monitoring, and runbook reports.
- **`audit_parity_implementation_report.md`.** The `./server ops audit parity` Go subcommand report.
- **`worktree_inventory.md`**, **`worktree_cleanup_report.md`**, **`worktree_cleanup_phase2_report.md`.** Worktree cleanup logs.
- **`docs_drift_fix_report.md`.** CLAUDE.md drift fixes.
- **`backup_tooling_implementation_report.md`** lives in the `tack-backup-restore-test` worktree, NOT in this directory. It is the input for TACK-228.

### 9.3 Files deleted on 2026-05-10

These were removed when the incident dir was reorganized. Listed here so the next agent does not look for them.

- **Tarballs** totaling about 83 MB: `fdb-snapshot-20260509T051802Z.tar.gz` (the first verified-restorable FDB backup since 2026-04-25), `audit-snapshot-20260509T164222Z.tar.gz` (pre-Phase-1-deploy audit ledger), and three forward-fix recovery snapshots. Deleted because the production backup chain at `/root/backups/tack-20260509T232955Z/` now serves the same purpose.
- **`manual_audit_backup/`** subdirectory holding the raw WAL binary `wal_20260509T055731.bin` from the audit drop investigation. Raw production audit data; the investigation conclusions live in `audit_table_investigation.md`.
- **`state_repair_workdir/`** subdirectory with the state repair agent's working JSON files.
- **`audit_log_first_refactor_plan.md`.** Rejected approach (synchronous Yugabyte writes) superseded by `audit_two_phase_plan.md`.
- **`audit_scale_research.md`.** First-pass research the operator said was off-target; superseded by `audit_scale_architectures.md`.
- **`audit_horizontal_design_revision_notes.md`.** Transient revision notes from the Redpanda-to-Kafka switch; the changes are already in the main design doc.
- **`phase1_verification_checklist.md`.** Verification checklist for Phase 1 deploy; Phase 1 is shipped.
- **`retro_update_report.md`.** Meta-report about retro updates; the retro log itself has the content.
- **`execution_snapshots_manifest.txt`**, **`execution_status.txt`**, **`state_repair_status.txt`.** Small post-completion status files.

### 9.4 Memory files (operator policies, persist across sessions)

- `/Users/agoodkind/.claude/projects/-Users-agoodkind/memory/feedback_plain_english_over_jargon.md`
- `/Users/agoodkind/.claude/projects/-Users-agoodkind/memory/feedback_always_verify_agent_outputs.md`
- `/Users/agoodkind/.claude/projects/-Users-agoodkind/memory/feedback_tack_qa_env_required.md`
- `/Users/agoodkind/.claude/projects/-Users-agoodkind/memory/project_tack_backup_system_broken.md`
- `/Users/agoodkind/.claude/projects/-Users-agoodkind/memory/project_tack_address_index_design.md`
- `/Users/agoodkind/.claude/projects/-Users-agoodkind/memory/project_tack_audit_drop_bug.md`
- `/Users/agoodkind/.claude/projects/-Users-agoodkind/memory/feedback_tack_deployment_landmines.md`

---

## 10. Critical decisions (settled, do not relitigate)

Each of these is a decision that came up during this session, was discussed, and has a final answer. Do not reopen.

### 10.1 Apache Kafka over Redpanda or NATS JetStream
Apache Kafka 4.x in KRaft mode is the audit broker.

Why not Redpanda. The BSL license is incompatible with the operator's 100% open-source rule.

Why not NATS JetStream. Jepsen 2025-12-08 found unresolved silent-data-loss findings (issues #7549, #7556, #7564, #7567, #7545). For a compliance audit ledger, "we acknowledged it, it's lost, and we kept accepting writes" is exactly the failure mode the system exists to prevent. Documentation was updated for the lazy-fsync case but the dangerous default remains, and the obvious mitigation (`sync-interval=always`) does not eliminate the data loss per #7545.

Why Kafka. Apache 2.0, KRaft eliminated Zookeeper as of 4.0, no comparable published silent-data-loss findings, largest community.

Sources: [jepsen.io/analyses/nats-2.12.1](https://jepsen.io/analyses/nats-2.12.1), [Kafka 4.2.0 release](https://kafka.apache.org/blog/2026/02/17/apache-kafka-4.2.0-release-announcement/).

### 10.2 SeaweedFS or Garage over MinIO or S3
Object storage for cold archive will be SeaweedFS or Garage (both open source). The final pick is deferred until Phase 2 wave 4.

Why not MinIO. Entered maintenance mode in late 2025, per the open-source-alternatives 2026 review.

Why not S3. Proprietary managed cloud service; the operator wants self-hosted.

### 10.3 Mac-side ops binary using DOCKER_CONTEXT=tack over SSH
All ops tooling that talks to production runs on the operator's Mac and uses Docker context `tack` over SSH. There is NO docker socket bind-mount on the production app container.

Why. Adding a docker socket bind-mount is a real production trust change. The operator rejected it during the consolidation plan review.

### 10.4 Image-based deploy
Build the application image locally on the operator's Mac with the Docker Go SDK. Save the image to a tar stream. Transport the tar stream to the production host over SSH using `DOCKER_CONTEXT=tack`. Load the image on the remote with the same SDK. Run `docker compose up -d` on the remote so the new image takes traffic. There is no container registry anywhere in this flow because Tack does not run one.

Why. Today's `make deploy` rsyncs the entire source tree to `/root/tack` and runs `docker compose build` on the remote. This violates the no-hand-rsync rule and creates the cleanup work tracked in TACK-234.

### 10.5 All ops tooling under ./server ops (monolith)
One binary. New subcommands at `./server ops backup`, `./server ops backup verify`, `./server ops backup restore-test`, and `./server ops deploy`. NOT a separate `tackctl` binary.

Why. Spirit of monolith. The existing `audit parity` family already established the pattern.

### 10.6 All tests run inside Docker
The new `make test-unit` target runs `go test ./...` inside `Dockerfile.test`'s image. It reuses `docker-compose.test.yml`. The backup agent set this up.

Why. Eliminates "works on my machine" drift and gives a deterministic CI-equivalent locally.

### 10.7 Address index removal over Option C hybrid
Delete the `address_index` family entirely under TACK-229. Route through `node_by_property`, which is already org-scoped.

Why. The original design decision report recommended Option C (hybrid scoping). The operator pushed back: framing it as "global vs scoped" mixes auth-layer concerns into a storage-layer decision. The cleaner answer in the "everything is a node" architecture is to delete the redundant index and use the property index that already exists. No backfill is needed because the address data is also on the underlying nodes as properties.

### 10.8 Docker Go SDK for deploy (mandated by operator, not up for litigation)
The deploy subcommand uses the Docker Go SDK at `github.com/docker/docker/client` for every Docker-related operation. The operator has confirmed that the SDK is the idiomatic and correct approach for Tack, and the decision is not open for further debate. Any agent that wants to switch this to a CLI shell-out approach must first clear that change with the operator directly.

Why. The SDK is the conventional Go binding to Docker. It returns structured types and Go errors instead of forcing the caller to parse text output from a subprocess, and it is what most production Go codebases that talk to Docker use. The previous deploy agent began considering a switch to `os/exec` invocations of the `docker` CLI in response to a `govulncheck` failure during `make build`, but that switch was never approved and was halted before any of the SDK-using files were rewritten. The SDK code that already exists in main is the shape we keep.

Status. The `govulncheck` failure during `make build` is the only open issue, and it is a `make build` configuration question rather than an SDK question. The next agent must investigate the `govulncheck` failure and resolve it without abandoning the SDK. Section 12 documents the concrete paths to investigate.

### 10.9 Phase 1 production-deployable, runbook revised
The wave 1 audit code at `a5aec6d` is committed. The wave 1 runbook at `1a06138` is revised against real verb names, log lines, and metric names. Deploy under TACK-231 can proceed once TACK-228 (the lint pass that unblocks `make build`) lands and the operator approves.

### 10.10 Plain-English explanations for incident-class decisions
When explaining tradeoffs or design choices to the operator, lead with what the user-visible behavior is, not the mechanism. "When the database goes down, do you want audit writes to silently buffer to disk or to return an error to the caller?" beats "the gate-of-64 channel only blocks under high concurrency".

### 10.11 Phase 2 wave 2 is hard cutover with rename, not gradual
The original wave 2 cutover plan (then named `wave2_cutover_plan.md`) described a gradual cutover with parity gates, soak windows, and an `AUDIT_RECORDER_MODE` env var as a rollback knob. The operator dropped the gradual approach on 2026-05-09 in favor of a hard cutover with rename and smoke tests. The reasoning is that Tack has no external clients yet, so a brief audit-availability gap during the rename is acceptable, and the hard cutover is much simpler operationally. The plan document was rewritten on 2026-05-10 as `audit_db_to_kafka_cutover.md`, and the original was deleted; the renamed document and the TACK-232 ticket description are the authoritative wave 2 plan. The hard cutover renames `audit.events_v2` to `audit.events` and `audit.events_v2_dlq` to `audit.events_dlq` in a single migration, then flips the producer to Kafka-only mode, then validates with a five-call smoke test. If anything fails the smoke test, the migration reverses the renames and restarts the producer in dual-write mode.

### 10.12 The "everything is a node" architecture has a real leak in domain code
The 2026-05-09 conversation surfaced that `internal/domain/node/types.go` has three hardcoded type-specific ID-derivation functions (`OrgID`, `WorkspaceID`, `SystemPropID`) that violate the architecture's stated principle that node types are data, not code. The operator flagged this directly when reviewing the workspace concept: workspace is supposed to be just a node with `nodeType="workspace"`, but having a function literally named `WorkspaceID` makes it a first-class concept the compiler knows about. The fix is one generic `NodeID(parentScope, address)` function that works the same way for every node type. This is filed as TACK-242 and should land after TACK-230 (the OrgID-specific narrow fix) and TACK-229 (the address-index removal), so the layered refactor goes from acute bug to broader cleanup in the right order. The takeaway for future architecture reviews: any function whose name contains a specific node type is suspect; the architecture's "type list is data" rule is what the codebase should match.

---

## 11. What NOT to do (with full reasoning)

### 11.1 Do NOT commit scripts/backup-content-check.sh
The operator explicitly blocked committing this. The fix is for a SIGPIPE bug, where `grep -q` exits early which sends SIGPIPE upstream which trips `set -euo pipefail`. Subshell-wrap with `set +o pipefail` resolves it. The fix has been hand-rsynced to CT 117. The script will be deleted when TACK-228 plus the backup tooling ship. Keeping the diff in the working tree is fine; committing it is not.

### 11.2 The docs/incidents/2026-05-09-seed-parallel-org/ directory is safe to commit (post-cleanup)
Before the 2026-05-10 reorganization, the incident directory lived at the repo root and held production tokens, raw audit WAL binaries, FDB snapshot tarballs, and repair manifests with real node IDs and issue names. The cleanup deleted all of that and moved the surviving narrative documents under `docs/incidents/`. The directory is now safe to commit, and the rule is the inverse of what it used to be: do commit it, so the next agent has the handoff in source control rather than as untracked working state. Run `gitleaks` against any new content added to this directory before committing, just to be sure.

### 11.3 Do NOT add raw production data to docs/incidents/
The 2026-05-10 cleanup removed four artifacts that contained production data: `state_audit_full_impact.md`, `state_audit_affected_rows.csv`, `undeclared_props_affected_nodes.csv`, and the `repair_artifacts/` directory tree. They held real node IDs, issue and epic names, comment titles, and repair manifests. Future incident documentation should describe what happened in narrative form, with redaction or aggregation for any production data that has to appear inline. Do not check in CSV exports of production rows; do not check in audit ledger snapshots; do not check in seed output that includes a token.

### 11.4 Do NOT deploy the new backup tooling to production YET
Wait until TACK-228 (the mechanical lint-fix pass that unblocks `make build` on the new Go backup tooling) lands AND end-to-end verification against real production data has happened. The verification is preferably in QA, which is being stood up under TACK-235. If QA is not standing yet, a `./server ops backup verify /root/backups/tack-20260509T232955Z` smoke test against the existing good backup is the smallest viable check.

### 11.5 Do NOT change internal/audit/wal.go or internal/audit/wal_test.go
Phase 1 just shipped at `23ad44a`. The fix is verified live in production. Do not regress it.

### 11.6 Do NOT delete the existing Makefile deploy and backup targets
Until TACK-233 is proven and an operator-approved cutover happens, the current Makefile targets stay grandfathered. TACK-233 is the replacement that ships an image-based deploy as `./server ops deploy`: build the image locally with the Docker Go SDK, save it to a tar stream, transport it to the production host over SSH using `DOCKER_CONTEXT=tack`, load it on the remote with the same SDK, and run `docker compose up -d` so the new image takes traffic. There is no container registry in the flow because Tack does not run one.

### 11.7 Do NOT run seed against production
The 2026-05-09 incident root cause was a seed pass during transition. The seed function at `cmd/server/seed.go:47` now has an unconditional hard-exit guard that refuses to run and references TACK-230. The guard stays in place until TACK-230 lands and replaces it with a proper seed-against-empty-database check. Until then, seed cannot run anywhere because the guard fires before any other code path. The dependency chain is TACK-230 (fix OrgID derivation, fold in seed prod-guard) before TACK-229 (delete the redundant `address_index` FDB family) before any other multi-tenancy work that touches the seed code path.

### 11.8 Do NOT act on the address_index design decision report's Option C recommendation
The operator's correct framing is TACK-229 (remove the redundant `address_index` family at `keys.go:140-142` and route address lookups through the existing org-scoped `node_by_property` index at `keys.go:124-127`; no backfill needed because the data is also already on the underlying nodes as properties). Read the report for context, but the implementation issue is TACK-229.

### 11.9 Do NOT use nolint directives or modify lint baselines
The `agent-gate` hook blocks both. Fix the code; do not silence the linter.

### 11.10 Do NOT replace the Docker Go SDK with os/exec or any other CLI shell-out
The operator has mandated the Docker Go SDK at `github.com/docker/docker/client` as the deploy subcommand's Docker integration, and that decision is not up for litigation. The previous deploy agent considered a switch to `os/exec` invocations of the `docker` CLI when `govulncheck` flagged daemon-side CVEs, but the operator has since confirmed that the SDK stays. The `govulncheck` failure is real and needs fixing, but it is fixed without abandoning the SDK; section 12 lists the investigation paths the next agent should pursue.

### 11.11 Do NOT push to remote
Do not run `git push` for any branch in this session's work without explicit operator approval. Commits stay local until reviewed.

### 11.12 Do NOT write em-dashes, en-dashes, or bare double-hyphens in prose
The `agent-gate` hook blocks them. The actual rule is sentence structure (real subject-verb sentences, no fused thoughts), but the surface-level enforcement is character-based.

---

## 12. The deploy subcommand work (incomplete, in main checkout)

This section documents the state of the partially-built deploy work and the one open question that remains. The Docker Go SDK is the chosen and operator-mandated approach for this subcommand. The work that the next agent picks up here is finishing the SDK-based implementation and resolving the `govulncheck` failure that currently prevents `make build` from passing.

### 12.1 Files that exist in main checkout (untracked)
- `internal/ops/deploy.go` (top-level dispatcher).
- `internal/ops/deploy_build.go` (builds the application image locally with the Docker Go SDK).
- `internal/ops/deploy_build_tar.go` (saves the built image to a tar stream).
- `internal/ops/deploy_build_test.go`.
- `internal/ops/deploy_help.go`.
- `internal/ops/deploy_integration_test.go`.
- `internal/ops/deploy_pull.go` (loads a saved image stream onto the remote via the Docker Go SDK over `DOCKER_CONTEXT=tack`).
- `internal/ops/deploy_push.go` (saves the locally built image and transports it to the remote over SSH; the file is named "push" but there is no registry push because Tack does not run a registry).
- `internal/ops/deploy_up.go` (runs `docker compose up -d` on the remote via the SDK so the loaded image takes traffic).
- `internal/ops/deploy_verify.go` (asserts that the running container's image digest matches what was just loaded).
- `internal/ops/deploy_verify_test.go`.
- `internal/ops/dockerctl.go` (shared Docker Go SDK helpers used by every deploy step; this is the file whose transitive imports `govulncheck` flags).

### 12.2 Files modified in main checkout
- `go.mod` and `go.sum` (Docker Go SDK dependencies plus their transitive closure).
- `internal/config/config.go` (deploy env vars).
- `internal/ops/command.go` (deploy family dispatch arm).
- `internal/ops/command_help.go` (help text for the new subcommand).

### 12.3 State at stop time
The agent had completed the SDK-based implementation across the deploy files and had just run `make build` to verify it. The build failed at the `govulncheck` stage because `github.com/docker/docker/client` transitively imports daemon source code that has known CVEs published against it. The agent then started a conversation about whether to switch from the SDK to an `os/exec` approach. The operator interrupted that conversation and confirmed that the SDK is the correct and idiomatic choice for Tack and that no switch is to happen. The deploy agent was stopped before it began rewriting any of the SDK-using files. The current files in main are SDK-based and represent the implementation we want to keep; they do not build only because the `govulncheck` configuration has not yet been resolved.

After session resume on 2026-05-10, the files are still untracked in main checkout in exactly the same shape (verified via `git status`); nothing has changed since the agent stopped.

### 12.4 What the next agent must do
The next agent's job is to investigate the `govulncheck` failure and resolve it without abandoning the Docker Go SDK. There are four investigation paths, and at least one of them is expected to succeed.

The first path is that `govulncheck` is reachability-aware. It does whole-program call-graph analysis and reports each finding with an "affecting" classification based on whether your code actually reaches the vulnerable function. If the daemon-side CVEs are flagged as unreachable from our `cmd/server` main package, then `govulncheck` itself is already filtering them, and the failure is in how `make build` parses `govulncheck` output rather than in the SDK. The agent should look at exactly what `make build` is treating as a failure and confirm whether it is reacting to unreachable findings.

The second path is that the specific CVEs probably have fixes in newer SDK versions. The Docker daemon ships security releases regularly, and the client SDK gets bumped along with them. Pinning `github.com/docker/docker` to the latest minor version may remove the findings entirely without any code change.

The third path is that the SDK has multiple import surfaces. The full `github.com/docker/docker/client` does pull a lot of daemon code, but narrower import paths (`github.com/docker/docker/api/types` plus a hand-written transport, for example) may give us the operations we need without the daemon code in the dependency tree.

The fourth path is that `govulncheck` itself supports an explicit ignore mechanism for findings the operator has reviewed and accepted. The `agent-gate` hook blocks the broad `//nolint` family, but it may not block the more specific `govulncheck`-only directive. The agent should confirm the hook's behavior before assuming this path is closed.

### 12.5 TACK-233 owns the eventual completion
This work is tracked under TACK-233. The ticket replaces `make deploy` with `./server ops deploy`. The new flow builds the image locally on the operator's Mac with the Docker Go SDK, saves it to a tar stream, transports it to the production host over SSH using `DOCKER_CONTEXT=tack`, loads it on the remote with the same SDK, and runs `docker compose up -d` so the new image takes traffic. There is no container registry in the flow because Tack does not operate one. The next agent should update that issue's description with the post-handoff state when work resumes.

---

## 13. The next agent's first move

### 13.1 Step 1: Read context (in this order)
1. This handoff doc.
2. `/Users/agoodkind/Sites/tack/AGENTS.md` (which is `CLAUDE.md`). The project's coding rules.
3. `/Users/agoodkind/Sites/tack-backup-restore-test/docs/incidents/2026-05-09-seed-parallel-org/backup_tooling_implementation_report.md`. The TACK-228 starting point.
4. `/Users/agoodkind/Sites/tack/internal/audit/clock.go`. The pattern for `time.Now()` indirection.

### 13.2 Step 2: Spawn the TACK-228 lint pass agent

Use this prompt verbatim:

```
You are working in an isolated git worktree at /Users/agoodkind/Sites/tack-backup-restore-test.
All your file paths are relative to that worktree root or use that absolute path.
Do NOT edit anything in /Users/agoodkind/Sites/tack (that is the main checkout).

This is TACK-228, P0. Your job is a mechanical lint-fixing pass to unblock make build
on the backup tooling.

## Context you need

The backup tooling implementation lives as untracked files in the worktree:
- internal/ops/backup.go (dispatcher)
- internal/ops/backup_run.go (orchestrator)
- internal/ops/backup_fdb.go
- internal/ops/backup_yugabyte.go
- internal/ops/backup_temporal.go
- internal/ops/backup_meilisearch.go
- internal/ops/backup_manifest.go (and test)
- internal/ops/backup_verify.go (and test)
- internal/ops/backup_restore.go
- internal/ops/backup_restore_fdb.go
- internal/ops/backup_restore_pg.go
- internal/ops/backup_restore_meili.go
- internal/ops/backup_dockerctl.go

Plus modifications to internal/ops/command.go, internal/ops/command_help.go,
internal/config/config.go, Makefile, go.mod, go.sum.

## Read first

1. The implementation report at docs/incidents/2026-05-09-seed-parallel-org/backup_tooling_implementation_report.md
   in this worktree. Lists every lint finding by category.
2. /Users/agoodkind/Sites/tack/AGENTS.md (the coding rules).
3. /Users/agoodkind/Sites/tack/internal/audit/clock.go (the pattern for time.Now indirection).

## What needs to happen

Run make build first to see the actual lint output. Then fix:

1. Every fmt.Errorf return missing a paired slog.Error.
   Pattern: emit slog.ErrorContext(ctx, "noun.verb.failed", "err", err, ...) before
   the return. Use named attrs only. Use noun.verb dotted names for events.

2. Every slog.Error or slog.ErrorContext call missing an err attribute. Add it.

3. Every bare time.Now() call. Add internal/ops/clock.go modeled on
   internal/audit/clock.go (the analyzer exempts files named clock.go from the
   time_now_outside_clock rule). Route deploy and backup time calls through it.

4. The miscellaneous findings:
   - gosec G305 on tar extract: validate extracted paths are inside the destination
     directory before writing. Use filepath.Clean and string-prefix-check.
   - godoclint formatting: fix doc comment formatting in place.
   - wrapcheck on gzip.Reader.Read: wrap the error with fmt.Errorf and a context
     message before returning.

## Constraints

- No //nolint directives. The agent-gate hook blocks them.
- No baseline edits. make baseline is gated on operator confirmation.
- No new shell scripts.
- No em-dashes, no en-dashes, no bare double-hyphens in prose.
- Real sentences with subjects and verbs.
- Build, lint, and test must all pass before reporting done.

## Verification gates

- make build passes (vet, golangci-lint, gocyclo, deadcode, staticcheck-extra,
  govulncheck, link).
- make lint-diff reports zero new findings.
- make test passes.
- gofmt -l empty across all touched files.

## Output

Write a final report at docs/incidents/2026-05-09-seed-parallel-org/tack228_lint_pass_report.md
listing per-file changes, before-and-after lint counts, and any decisions you
had to make.
```

### 13.3 Step 3: After TACK-228 (lint pass agent) reports
1. Verify-don't-trust. Run `make build` yourself. Confirm zero new findings. Run `make test`. Run `gofmt -l`.
2. Rebase the worktree onto main. Currently at `dd430c9`, main is at `1a06138`. Two commits ahead: `a5aec6d` (Phase 2 wave 1 audit) and `1a06138` (runbook revision). Rebase should be conflict-free since the worktree only touches `internal/ops/backup_*.go` and a few shared files; resolve any conflict in `internal/ops/command.go` (both branches add to the family dispatch).
3. Commit the backup tooling work. Single logical commit. Use a subject line like `Add ./server ops backup family for image-based DR tooling`.
4. Pick up the deploy subcommand work. The Docker Go SDK is the operator-mandated approach, and the SDK-based code already exists in the main checkout. The next agent's job is to investigate and resolve the `govulncheck` failure that currently blocks `make build` for those files; section 12 lists the four investigation paths to pursue.

### 13.4 Step 4: After backup tooling lands, cleanup and stabilize before opening any new workstream
The operator has been explicit that the order is backup tooling first, then cleanup-and-stabilize, then QA, then everything else. Cleanup-and-stabilize means TACK-234 (the Deliverable 3 cleanup: delete the four shell scripts, rewire the systemd timer for restore-test to invoke the Go binary, remove or wrap the Makefile deploy and backup targets, wipe `/root/tack` so the production host holds only `docker-compose.yml` and `.env`), plus an end-to-end smoke test of the Go backup tooling against the existing good backup at `/root/backups/tack-20260509T232955Z`, plus deleting the orphan `phase2-wip` branch that section 7.4 documents. Only after cleanup-and-stabilize is signed off should TACK-235 (the QA environment standup) and TACK-231 (the Phase 2 wave 1 production deploy) start. TACK-235 and TACK-231 can run in parallel with each other once cleanup is done, because QA is the recommended gate before wave 1 ships to production but does not technically block it.

---

## 14. Glossary (alphabetical, terms not already defined in section 2)

- **CT 117.** The LXC container hosting Tack production at `3d06:bad:b01::117`. SSH alias `tack`.
- **DR.** Disaster recovery.
- **GHCR.** GitHub Container Registry. Mentioned in some older planning docs as a deploy transport, but the operator has confirmed there is no Tack registry; the deploy flow uses `docker save` piped over SSH to `docker load` instead.
- **govulncheck.** The Go standard vulnerability scanner. Run as part of `make build`. It does whole-program reachability analysis, so each finding is classified as either "affecting" (your code reaches the vulnerable function) or unreachable. The `make build` target's exact handling of unreachable findings is one of the open questions the next agent has to resolve so that the Docker Go SDK can stay in `go.mod` without blocking the build.
- **KRaft.** Kafka's consensus protocol replacing Zookeeper. Default in 4.0+.
- **`org_members`.** SQL table in YugabyteDB. Auth gate: who is allowed in which org.
- **OrgID.** UUID identifying a tenant. Currently deterministic from slug at `internal/domain/node/types.go:288-290` via `uuid.NewSHA1(orgNamespace, []byte(slug))` with no tenant input, which is the bug TACK-230 fixes by switching to random UUIDv7 so two customers with the same slug do not produce identical UUIDs.
- **`/root/tack`.** The current source-tree mirror on CT 117. Wiped by TACK-234.
- **SHA-256 manifest.** Backup integrity file at `<backup>/MANIFEST.txt`. One line per file with sha256, size, and relative path.
- **wave 1, wave 2, wave 3, wave 4.** Phase 2 deploy waves. Wave 1 is dual-write (code at `a5aec6d`, not deployed). Wave 2 is hard cutover to Kafka primary (planned in `audit_db_to_kafka_cutover.md`). Wave 3 is consumer scale (planning-only). Wave 4 is cold archive (planning-only).
- **YugabyteDB.** PostgreSQL-compatible distributed SQL. Holds auth and audit only.

---

## 15. End

If anything in this doc references a concept that is not defined, that is a defect. File it as a comment at the bottom of this file rather than ignoring it. The next agent operates on this doc as ground truth, so missing definitions become bugs in the handoff.
