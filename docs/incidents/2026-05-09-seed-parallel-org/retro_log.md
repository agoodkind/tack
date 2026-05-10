# Incident Retro Log: Seed-Created Parallel Org

**Date:** 2026-05-09
**Status:** Forward-fix executed. Phase 1 audit-drop fix deployed and verified end-to-end (commit `23ad44a`, deployed `phase1-wal-fix` build at 22:32 UTC). Read-class audit events landing again. Maintenance window can close. Resolution detail in section 11.
**Author role:** Retro scribe (neutral, factual)
**Incident scope:** Production Tack server on CT 117

---

## 1. Incident summary

On 2026-05-09, Tack appears to be fully unusable from a user-facing standpoint while
all server processes are still running and HTTP is still responding. The trigger
appears to have been the wave 1 deploy of the slug-to-generic-address migration
(commit `dd430c9`), followed by a `seed` run that, based on the post-deploy state,
created a parallel org and parallel workspace alongside the legacy production
records rather than reusing the existing ones. The generic `address_index` for the
production references `goodkind-io` and `main` now points at the new empty
entities, so MCP `tack_describe_workspace {"workspace_reference":"main"}` resolves
to an empty workspace and `tack_get_project` against legacy IDs fails with
`unknown reference strategy "direct_slug" for project`. From the user's point of
view, "Tack is fully down."

A second, independent finding surfaced during incident response that materially
changes the recovery posture and the team's ongoing risk profile. It is captured
in the next section.

---

## 1A. Critical addendum: production has no valid FDB backups

During incident response, a subagent attempted to validate
`/root/backups/tack-20260509T042729Z` as a possible recovery source. The
attempt produced a definitive negative finding that affects far more than
this incident.

### Finding

The pre-deploy backup tarball contains zero FDB data files. Specifically,
`tack_fdb-data.tar.gz` has 66 entries, none of which are the `.sqlite` or
`.fdq` files that hold the actual database. The named volume `tack_fdb-data`
mounted at `/var/fdb` only holds `lib/`, trace logs, and an empty `data/`
directory. The real ~211 MB of database files lives in an anonymous Docker
volume (ID `7a90eb88d56c...`, full ID captured in the test report) that the
backup script never references.

### Mechanism

The `foundationdb/foundationdb:7.4.6` image declares `VOLUME /var/fdb/data`
in its Dockerfile. When the container starts, Docker honors that declaration
by creating an anonymous volume mounted at `/var/fdb/data` that shadows the
named `tack_fdb-data` mount at the parent path. The data files therefore
end up in the anonymous volume, not the named one. The backup script tars
the named volume and reports success, but the data path inside it is empty
because the anonymous volume is mounted on top of it.

### Scope

The anonymous volume holding the real data was created on 2026-04-25.
Every backup the script has produced since that date has the same defect.
That is approximately two weeks of backups that exist as files on disk
but contain no FDB data. The team has been operating production with no
restorable FDB backup for that entire window.

### Consequence for this incident

Backup-restore is not a viable recovery path for this incident. The only
path forward is the forward-fix described in section 9. The forward-fix
playbook is being drafted by a parallel subagent.

### Consequence beyond this incident

This is an ongoing operational risk that is unrelated to today's seed
issue. Until a real backup process is in place, any FDB-affecting incident
is unrecoverable except by manual reconstruction. Ranked by severity this
is at least as serious as today's user-visible outage, because today's
outage is recoverable while a future FDB data loss event currently is not.

### Required follow-ups

- Take a real FDB backup immediately, before any forward-fix mutation. A
  parallel subagent has been spawned to run `fdbbackup` against the live
  cluster and to tar the anonymous volume by ID. The fdbbackup output is
  the authoritative copy; the volume tar is a belt-and-suspenders fallback.
  A checksummed copy is to be brought back to the operator's local machine
  as an offsite copy.
- Replace `scripts/backup.sh` with a proper FDB backup mechanism. Options
  include `fdbbackup` driven from inside the container, a sidecar that
  uses the FDB client API, or, less cleanly, overriding the `VOLUME`
  declaration in the compose file so the named volume actually holds the
  data and live-tar regains its (already weak) consistency caveat.
- Apply the same scrutiny to Yugabyte. The script tars the live Yugabyte
  data directory, which has the same consistency caveat. The CSV dumps of
  `users`, `api_tokens`, and `org_members` are the only auth-table backups
  that should be considered authoritative today.
- Add a test that fails CI if `tack_fdb-data.tar.gz` is missing the
  expected file types after a dry-run backup. The current backup script
  has been silently failing without raising any alert.
- Audit any other place where the existing backup is referenced as a
  recovery resource, in docs or runbooks, and correct it.

### Sensitive-data note for this addendum

The anonymous volume ID is non-secret. The exact file inventory inside it
is non-secret. If any subsequent backup procedure produces logs that
contain credentials (database passwords, FDB cluster files in some
deployments), redact them before retaining the logs.

---

## 1D. Architectural finding: audit.events table does not exist in production

A subagent running the state-repair lane on 2026-05-09 reported that
`audit.events` is missing from the production YugabyteDB instance. This
contradicts the SQL contract documented in CLAUDE.md.

### Finding

The subagent enumerated the database schemas and table layout in
production Yugabyte while preparing to verify whether the prior
2026-05-07 state repair had emitted audit events. It found no `audit`
schema and no `audit.events` table.

CLAUDE.md documents `audit.events` (plus `audit.chain_heads`,
`audit.notarizations`, `audit.pii`) as the compliance audit ledger.
The whole design treats Yugabyte as the durable home of auth and
compliance audit. Migrations under `migrations/` include an audit
schema migration (`002_audit.sql`) that the deployed binary ships.

### Implications

- Compliance audit is not actually being recorded. Any work that
  expected to land an `audit.events` row, including the prior state
  repair on 2026-05-07, has no provenance trail.
- The earlier audit-lane work captured by tickets TACK-169 through
  TACK-178 (audit subsystem 1 through 10 of 10) is shown in MCP as
  Done. The ledger they were built around is not present in production.
  Either the migration has not been run, or it has been run and the
  schema is being dropped or filtered somewhere, or the deployed binary
  is not writing what the design says.
- This is at minimum a documentation drift, and at most a complete
  gap between the documented compliance contract and what production
  is doing.

### Required follow-ups

- Confirm whether the audit migration has been run against this
  Yugabyte instance. Inspect `schema_migrations` (or whichever
  migration tracking table is in use) for the `002_audit.sql` row.
- If the migration has not been run, run it. The deployed binary
  expects the schema. Other audit-emitting code paths may be silently
  noop-ing.
- If the migration has been run and the tables are nevertheless
  missing, investigate where they went. Possibilities include a manual
  drop, a recovery operation that restored an older schema, or a
  superuser action that wiped the audit schema.
- Reconcile with the MCP-side audit tools (TACK-176) and the recorder
  package (TACK-170, TACK-171) so the gap between "ticket says done"
  and "table does not exist" closes.
- This issue is independent of the seed-parallel-org incident and
  should not block its closure, but it is at least equal in severity
  and warrants its own post-incident.

### Sensitive-data note

Audit table contents (when they exist) include compliance-sensitive
PII and notarization payloads. Once the table is restored or built,
treat any inspection of the actual rows as PII work.

---

## 1B. Architectural finding: address index is global, not org-scoped

During remediation playbook drafting, a subagent inspecting the FDB key
families found that the `address_index` implementation differs from the
documented design in CLAUDE.md. This is captured here because it shaped
the remediation plan and has implications beyond this incident.

### Finding

CLAUDE.md describes the address index as org-scoped. The documented key
is `node_address` keyed by `(orgID, scopeID, nodeType, addressKind, addressValue)` returning `nodeID`.

The actual implementation in `internal/adapters/foundationdb/keys.go`
lines 140 to 142 is global. The implemented key is `(address_index, nodeType, addressKind, address)` returning `nodeID`.

The `orgID` and `scopeID` components do not exist in the implemented key.
The index is global across all tenants.

CLAUDE.md also documents a `node_address_by_node` reverse index. That
keyspace does not exist in the current code base. There are no references
to it under `internal/`. Reverse lookup from a node to its addresses
cannot be done efficiently without it.

### Tradeoffs of the current global design

Pros:

- Simpler resolver code. No org context is required to resolve a
  reference, which matches the "globally addressable by UUID alone"
  principle stated in CLAUDE.md.
- Smaller index. One key per (type, address) pair instead of one key per
  (org, type, address) pair.
- Bootstrap is straightforward. An `org` reference can be resolved
  without first knowing its `orgID`, which is a chicken-and-egg problem
  any scoped design has to solve separately.

Cons:

- No multi-tenant isolation at the address layer. Two tenants cannot
  both have a workspace named `main`, a project named `TACK`, or any
  other identical reference value at the same node type.
- Cross-tenant collisions are silent. The first writer claims the slot.
  Subsequent attempts either overwrite, conflict, or get repointed,
  depending on the code path.
- The single-tenant assumption is not enforced anywhere. The code does
  not refuse to admit a second tenant; it simply breaks in subtle ways
  if one arrives.

### Tradeoffs of the documented org-scoped design

Pros:

- Multi-tenant isolation. Each org's reference namespace is independent.
- Matches the architectural principle that orgID is the tenancy root.
- No cross-tenant collisions are possible.

Cons:

- Org references themselves still need a global-or-bootstrap resolution
  path. Resolving an org reference cannot require an `orgID` because
  that is what the lookup is supposed to produce.
- More complex key shape. Larger index footprint.
- Migration cost. Every existing `address_index` row would need to be
  rewritten to include the new prefix components.

### A hybrid is defensible

`org` references are global by necessity, because they are the root of
the tenancy tree. Everything below `org` could be scoped to its parent
without breaking the bootstrap path. The current code does not implement
that hybrid. It is global at every level.

### Implications for this incident

- Today's incident was partially enabled by the global design. The index
  has exactly one slot for `(workspace, primary, "main")`. Seed claimed
  it for the new empty workspace. The legacy workspace lost its
  canonical reference even though the legacy record still exists in FDB.
  Under an org-scoped index, the new and legacy workspaces would each
  have their own slot under their own org, and the conflict would not
  exist.
- The forward-fix is still safe under the global design because the
  remediation plan touches only the two existing rows (`goodkind-io` and
  `main`) and rewrites them to point at the legacy IDs. The fix works
  only because there is currently only one tenant.

### Implications beyond this incident

- Multi-tenancy is broken at the address layer. The system cannot support
  a second tenant whose workspaces, projects, or issues happen to share
  reference values with the first tenant. That is a near-certain
  collision rather than an edge case, since tenants commonly use names
  like `main`, `web`, `api`, or `app`.
- Documentation drift exists in both directions. CLAUDE.md describes
  org-scoped keys and a reverse index. Code implements global keys and
  no reverse index. New contributors who follow CLAUDE.md will write
  code that expects features the system does not have and will not
  write code to handle the global-collision case the system does have.
- Scaling toward the product goal stated in CLAUDE.md ("a complete
  replacement for Plane CE / Linear / Jira", "horizontally scalable from
  day 0", "multi-tenant correctness") requires resolving this
  inconsistency before the second tenant lands.
- Operational diagnostics for "which addresses point at this node" are
  currently impossible without a full table scan, because the reverse
  index does not exist. Today the address index is small enough to scan,
  but at scale this becomes prohibitive.

### Required follow-ups

- Decide the target design: global, fully scoped, or hybrid (org global,
  everything else scoped). Capture the decision in CLAUDE.md and in an
  ADR or equivalent.
- If the target is scoped or hybrid, plan a migration of the existing
  `address_index` rows to include the new prefix components. This is
  similar in shape to the wave 1 migration that triggered today's
  incident, so apply the lessons from this retro to the migration plan.
- Implement the missing `node_address_by_node` reverse index, or remove
  it from CLAUDE.md if the target design no longer includes it.
- Add a code-and-docs consistency check that fails CI when CLAUDE.md key
  shapes drift from the actual key constructors in
  `internal/adapters/foundationdb/keys.go`. The check could be as simple
  as a parser that extracts both, or as careful as a property test that
  builds keys from each side and asserts they round-trip.
- Add an explicit assertion in seed and any other bootstrap code that
  refuses to admit a tenant whose root references would collide with an
  existing tenant's, until the multi-tenant address design is in place.

---

## 1C. Operational finding: no out-of-band QA environment exists

This incident made a missing operational capability visible. There is no
QA environment for Tack that lives outside production. Migrations,
deploys, seed runs, and backfills are validated only by local testing
against developer-shape data and then executed directly on production.

### Finding

The deploy pipeline rsyncs to a single host (CT 117). That host runs
production. There is no parallel host, container set, or data set that
mirrors the production shape and can be exercised before production is
touched. Local `make test` runs verify code correctness against unit
fixtures. They do not exercise the deploy, seed, or backfill paths
against data that resembles production.

For today's incident specifically:

- The seed planning gap (consult only `address_index`, not `slug_index`)
  is a behavior that only surfaces when there is real legacy data in the
  store. Local fixtures do not have legacy `slug_index` entries because
  they are created on a recent, not-mid-migration codebase.
- The backup-system defect (anonymous-volume shadowing) is a deploy-time
  behavior that only surfaces when an FDB image is run with the same
  Compose configuration as production. A local docker-compose run with
  the same image would have surfaced it, but no one ran one.
- The address-index global vs scoped behavior would have shown up the
  first time a second tenant or a duplicate reference value was seeded
  into a non-trivial environment. There is no environment in which that
  happens.

### Implications

- Every wave-1-style migration is a production experiment. The cost of a
  failed experiment is a maintenance window and a forward-fix. The team
  is currently absorbing that cost rather than avoiding it.
- High-blast-radius operations (seed during transition, backfill apply,
  state repair apply) cannot be rehearsed. The operator's first
  execution is the production execution.
- The team has no way to validate backup-and-restore end to end. Even
  after the backup system is rebuilt, a "verified test restore" requires
  a cluster to restore into. Spinning up a scratch cluster per test is
  expensive enough that it tends to be skipped.
- Multi-tenant correctness work cannot proceed safely. Adding a second
  tenant in production to test the address index would be reckless.
  Adding it in a QA environment is the right move, but no such
  environment exists.
- New contributors cannot exercise the full deploy path before merging.
  PR review is necessarily code-only, not behavior-validated.

### Required design

A QA environment that lives completely out of band from production, with
real-shape replicas of the current single-org production data. Concrete
properties:

- Separate host or set of hosts, separate Docker network, separate
  domain, separate certificates. No shared mounts, no shared FDB
  cluster file, no shared Yugabyte instance.
- Full Compose stack identical to production. Same image versions,
  same environment variables (with QA-appropriate values), same
  resource shapes.
- Real-shape data: a copy of the production org, workspace, projects,
  issues, types, properties, addresses, sequences, and audit
  references. PII can be redacted; structure must not.
- A repeatable refresh mechanism. The QA data should be re-seedable
  from a recent production snapshot on demand, so it does not drift
  from production over time. The snapshot mechanism should be the
  same as the new backup system once that is in place.
- A clear "this is QA" indicator at every layer. Logs, headers, MCP
  responses, and the dashboard if any. No reasonable way to confuse
  a QA call for a production call.
- An explicit policy that production migrations and high-blast-radius
  ops MUST be exercised in QA first. Runbooks list the QA validation
  step as a precondition.

### How this incident would have gone with QA in place

- Wave 1 deploy would have happened in QA first. Seed against the
  QA-replicated data would have created the parallel org in QA, not
  production. The forward-fix playbook would have been written and
  tested in QA. The empty-backup defect would have surfaced in QA when
  the rollback path was exercised, before production deploy. The
  user's outage today would not have happened.
- The backup system rebuild can be tested end to end in QA without
  risking production data. Test restores can run on a schedule in QA
  without burning the production cluster's I/O budget.
- The address index design decision can be validated in QA by seeding
  a second tenant and observing the collision behavior, then testing
  the chosen target design against the same scenario.

### Required follow-ups

- Stand up a QA host or container stack matching production
  configuration. CT or VM scope to be decided.
- Build a repeatable production-to-QA data refresh procedure. The
  output of the rebuilt backup system should feed directly into this
  procedure.
- Add QA to the deploy pipeline as a required stop before production
  for any change that affects schema, FDB key shapes, seed behavior,
  or migration paths.
- Add a runbook entry: every wave of a migration must be exercised in
  QA before production. The QA result is part of the wave's stop
  condition for production deploy.
- Add monitoring of QA environment freshness. Alert if QA data drifts
  more than N days from production.
- Decide retention for QA snapshots. They are derived data and can
  generally be ephemeral, but a small retained set helps with
  reproducing past incidents.
- Decide how to seed the second-tenant case in QA. The address index
  work in section 1B will need this. Pick a deterministic synthetic
  tenant identity that is obviously not a real customer.

---

## 1E. Operational finding: no incident-mode runtime control plane

The 2026-05-09 incident escalated from "seed misbehaved" to "production
MCP is fully down" because the operator had no surgical way to stop the
in-flight damage. The only available controls during the incident were
container restarts (too coarse, blast radius unclear) and code redeploy
(too slow, requires full deploy cycle). Fine-grained controls would
have shortened recovery and limited damage in this and future
incidents.

### Finding

There is no runtime control plane in Tack today. Every behavior is
either always-on or controlled by an env var that requires a restart
to change. There is no way to:

- Reject a specific verb from running (for example, "no more
  `node.create` until further notice").
- Reject writes to a specific node type (for example, "no writes to
  `org` or `workspace` while we investigate").
- Reject operations from a specific actor or org (for example,
  "isolate org X while we triage").
- Throttle a specific shard or partition (for example, "rate-limit
  reads on shard 3 while it backfills").
- Fail open or fail closed on the audit path during compliance
  triage.
- Pause the seed path (for example, "block any further `seed` calls
  while we determine if the existing parallel org needs cleanup").
- Drain in-flight work and refuse new work (graceful read-only mode).
- Redirect a specific reference to a different node id during
  recovery (for example, "until we fix it, route `goodkind-io` org
  reference back to the legacy UUID even if address index says
  otherwise").

The closest thing to runtime control today is `/server ops` commands,
which are point-in-time admin operations rather than persistent policy
that hot paths consult on every request.

### How this would have helped today

- During the post-seed window, an "incident mode" toggle could have
  blocked further writes to `node_type_def` for the new org while
  the operator confirmed the parallel-org hypothesis. Damage would
  have stayed at "two parallel records exist" rather than expanding.
- After root cause was identified, an "address override" toggle could
  have routed `goodkind-io` and `main` reference lookups back to the
  legacy UUIDs immediately, restoring user-visible MCP behavior in
  seconds instead of the 12-minute forward-fix.
- The 2026-05-08 state-repair incident (separate, but in the same
  codebase) had similar dynamics: precise control over which repair
  classes can run on which scopes would have allowed the operator to
  apply repairs incrementally with safer blast-radius bounds.

### Required design

A runtime configuration system with the following properties:

- **Hot-reloadable.** Policy changes take effect within seconds, no
  restart required. A SIGHUP, an admin endpoint, or a watched config
  file. Pick one and document.
- **Highly configurable per dimension.** Each policy decision can
  scope by any combination of: verb (`node.create`, `auth.*`, etc.),
  node type (`org`, `workspace`, `project`, custom types), node id,
  org id, actor id, scope id, request source (MCP, ConnectRPC,
  internal), shard, time of day, audit risk class. Combinations
  are AND-ed unless explicitly OR-ed.
- **Block, throttle, or redirect.** Each rule resolves to one of:
  reject the operation with a specific error, throttle to N per
  second, redirect to a different code path or different target
  identifier. Block and redirect are loud (logged, alerted, audited);
  throttle is quiet but counted.
- **Cheap on the hot path.** Lookups must be O(1) or at worst small
  O(rules) with caching. Atomic loads, no I/O, no mutex contention
  during steady state.
- **Audited.** Every policy decision that blocks, redirects, or
  throttles emits an audit event. Operators must be able to see what
  the control plane denied and why.
- **Default to off.** The control plane is empty by default. Explicit
  rules turn things on. No production-affecting default behavior.
- **Operator-friendly inputs.** Rules are written in a small DSL or
  structured config that an operator can author by hand during an
  incident, not via Go code. Validation is upfront: a malformed rule
  is rejected, not silently ignored.
- **Layered.** System rules take precedence over org rules take
  precedence over user rules. The precedence order is documented and
  the rule that matched is included in the deny response.
- **Test affordances.** Operators can dry-run a rule to see what it
  would deny without enforcing, and can scope a rule to a single test
  org or test user before promoting it to production.

### Concrete control surfaces to implement

The system should expose at least these surfaces, each addressable
via the control plane:

- Verb gate: deny or allow specific verbs.
- Node-type gate: deny writes or reads to specific node types.
- Org gate: deny all operations from or to a specific org.
- Actor gate: deny operations from a specific actor.
- Shard gate: deny or throttle operations on a specific FDB shard or
  Yugabyte shard.
- Audit-path mode: choose between fail-closed (block on audit
  failure), fail-open (proceed without audit), drop-with-counter
  (today's behavior, soon to be Phase 1's backpressure), or paused
  (refuse all new audit writes).
- Seed gate: refuse to run seed unless the operator explicitly
  authorizes the run.
- Migration gate: refuse to run any backfill or repair op without
  explicit authorization.
- Reference override: pin a specific reference to a specific node id
  regardless of what the address index says. Logged, audited, and
  expires automatically after a configurable TTL.
- Read-only mode: refuse all writes, system-wide or scoped.
- Maintenance mode: refuse all non-admin requests, return a specific
  error to the caller with operator-set message.

### Required follow-ups

- Add a control-plane design doc to the repo capturing the data model,
  the rule DSL, the precedence order, and the hot-path lookup
  algorithm.
- Implement a minimum-viable version covering verb gate, org gate,
  audit-path mode, and reference override. The other surfaces can
  follow.
- Wire the control plane into the existing audit path so blocks are
  audited.
- Add CI tests that exercise the control plane under realistic loads
  to verify the hot-path overhead is negligible (target sub-microsecond
  per request in steady state).
- Document the operational runbook for using each surface during an
  incident, with worked examples drawn from this incident and the
  2026-05-08 state repair incident.
- Decide whether the control plane should sync across multiple Tack
  instances when multi-host arrives. Likely yes; the design must not
  preclude it.

### Risks to watch

- A control plane is itself code that can have bugs. A misconfigured
  rule can take down production faster than the original incident
  did. The dry-run affordance and the layered precedence are the
  primary mitigations.
- Hot-path overhead must stay near zero. A naive implementation that
  iterates rules per request would regress the throughput target the
  Phase 2 audit refactor is built around.
- The control plane creates audit-path-during-audit-fail loops if not
  carefully designed. Audit events that describe a control-plane
  decision must not themselves go through the gates the control plane
  enforces, or else a "block all audit" rule becomes self-silencing.

---

## 1F. Operational finding: backup content-check has a SIGPIPE bug under pipefail

While verifying the rebuilt backup pipeline today, the content-check
script reported FAIL on backups that are actually fine. The error path
collapses to a SIGPIPE interaction with `set -euo pipefail` rather than
real corruption.

### Finding

`scripts/backup-content-check.sh` includes a `sniff_pg_dump` helper that
runs roughly `head -c 16777216 "$file" | grep -qE '^CREATE TABLE\b'`.
With `set -euo pipefail` enabled, `grep -q` exits 0 as soon as the first
match lands, which sends SIGPIPE upstream to `head`. `head` then exits
141. Pipefail surfaces that 141 as the pipeline's overall status, the
`if` arm goes to the `else`, and the helper reports "no match" even
though the data is fine.

Concrete demonstration on backup `tack-20260509T232955Z`:

- Two valid pg_dump files in the tarball: a 1.1 GB Yugabyte dump and a
  278 KB Temporal-DB dump. Both contain `CREATE TABLE` statements (19
  in the Yugabyte dump, several in the Temporal dump).
- `backup-content-check.sh` reported FAIL with "no CREATE TABLE
  statement found in first 16 MiB" for both files.
- A direct `head -c 16777216 file | grep -cE '^CREATE TABLE\b'` on the
  same file showed 18 matches in the first 16 MiB.

### Fix

Wrap the offending pipeline in a subshell that turns pipefail off for
the duration: `( set +o pipefail; head -c ... | grep -qE ... )`. After
the fix, all four content checks PASS on the same backup that
previously reported FAIL.

### Status of the fix

The fix exists in the local working tree but is **not committed**. Two
operator-imposed rules apply:

- No new shell scripts and no extensions to existing shell scripts. The
  content-check script is slated for replacement by a Go subcommand
  under `./server ops backup verify`. See section 1G.
- No hand-rsync of changes to the production host. The fixed script was
  hand-rsynced to CT 117 today as a one-off so today's verification
  could complete; that is the last time this happens. Any further
  change to production must come from a real deploy.

### Required follow-ups

- Move the content-check logic into the Go ops subcommand so the bug
  cannot recur in shell. The subcommand reads the tarball, decompresses
  the relevant entries, and applies the same sniff logic without
  pipefail interactions.
- After the Go subcommand lands, delete `backup-content-check.sh`
  rather than carrying both versions.
- Treat any future "backup verification reports FAIL" alert with
  suspicion until the Go-side check is in place. Confirm against the
  raw file before paging.

---

## 1G. Operational finding: shell-script moratorium

Today's incident response surfaced three classes of bug inside ad-hoc
shell scripts in this repo:

- The SIGPIPE under pipefail bug in `backup-content-check.sh`
  documented in section 1F.
- A missing `-C /etc/foundationdb/fdb.cluster` flag on `fdbbackup`
  invocations that left the command unable to find the cluster file in
  some deploys.
- A `pg_dump` versus `ysql_dump` binary name mismatch in the Yugabyte
  backup path. Yugabyte's installer ships the dump tool as
  `ysql_dump`; the script invoked `pg_dump`, which either resolves to
  the host's PostgreSQL client (different schema assumptions) or fails
  outright depending on the image.

The pattern is consistent: shell tooling has accumulated technical
debt that small fixes do not retire. Each fix risks introducing the
next class of subtle shell bug, and none of these scripts have unit
tests or even shellcheck gates.

### Operator decisions

Two rules took effect during this session:

- **No new shell scripts** in the repo. Existing scripts may receive
  no-op edits during their migration window, but no new functionality
  lands in shell.
- **No hand-rsync to the production host.** Anything on the
  production box must arrive through a real deploy. The one exception
  documented in section 1F is explicitly the last time it happens.

### Why this matters

The consolidation plan in section 1H moves backup, verify, and
restore-test logic under `./server ops` so the same Go test discipline
applies as the rest of the codebase. The moratorium is the holding
pattern that keeps the shell debt from growing while that work lands.

### Required follow-ups

- Inventory the existing shell scripts under `scripts/` and label each
  with its replacement target (which `./server ops` subcommand or
  Compose change retires it).
- Add a CI gate that fails on new files matching `scripts/*.sh` until
  the moratorium is lifted by an explicit decision.
- Document the deploy-only rule in the operational runbook, including
  the rationale and the one-off exception window for the content-check
  fix.

---

## 1H. Operational finding: ops consolidation plan launched

A plan agent produced
`/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/ops_consolidation_plan.md`
(936 lines) describing how to move backup, verify, and restore-test
logic under `./server ops`, replace `make deploy` with
`./server ops deploy` (image-based and registry-pushed), and impose a
Docker-only test discipline. The plan is reference material;
implementation has not started.

### Highlights from the plan

- A Mac-side Tack binary that runs `./server ops *` against the
  production host using `DOCKER_CONTEXT=tack` over SSH. The plan
  declines to assume a Docker socket bind-mount on the production app
  container because that would be a real change in the trust boundary.
- GHCR private registry as the steady-state image distribution path.
  `docker save | ssh | docker load` is the offline fallback when
  registry access is unavailable.
- A 6-step migration sequence, with QA-environment readiness as the
  pivot. If QA stands up first, run the full sequence. If QA is not
  ready, ship deliverables 1 and 3 first and defer 2 until QA exists.

### Why the plan exists

The shell moratorium in section 1G removes a tool the team was
implicitly relying on for incident-time fixes. Something has to take
its place. The plan describes that replacement so the moratorium does
not turn into "we cannot operate the system."

### Required follow-ups

- Schedule the deliverable 1 work (`./server ops backup`,
  `./server ops backup verify`, `./server ops restore`) ahead of any
  further shell changes.
- Decide the QA-environment readiness question (section 1C). The plan's
  ordering depends on it.
- Track plan execution as its own thread, separate from the incident.
  The plan is the followup, not the resolution.

---

## 2. Timeline (UTC, 2026-05-09 unless noted)

Times are approximate and reconstructed from the conversation transcript and
recent commit history. They will be tightened during the formal retro.

### Pre-incident (planning and preparation)

- **2026-05-08 (prior day).** Earlier separate state-repair incident produces
  artifacts under `repair_artifacts/2026-05-08-remaining-safe/` and
  `repair_artifacts/2026-05-08-final-report-package/`. These exist on disk on
  2026-05-09 but are not the cause of this incident; they are relevant only
  because they motivated the "clean deploy source" requirement. The artifacts
  were later deleted as part of the 2026-05-10 cleanup, because the work they
  documented had shipped and the manifests contained production data.
- **2026-05-08 / 2026-05-09 boundary.** Plan
  `/Users/agoodkind/.cursor/plans/finish-slug-and-state_74920a00.plan.md` is
  authored. It defines a two-wave rollout:
  wave 1 ships the generic address baseline and runs backfill + repair, wave 2
  removes legacy slug-only surfaces.
- **Local commits leading into wave 1.** `git log` shows the slug-to-address
  refactor landed across multiple commits, ending at `dd430c9` (`Add generic
  address backfill operations`), with prior commits including `91b51eb`,
  `758e651`, `df99a85`, `584176e`, and `82d39ad`. Local `main` is reported five
  commits ahead of `origin/main`.

### Incident day

Times below are best-effort UTC reconstructions; the only firm timestamp from
the transcript is the deploy backup directory name.

- **~04:13 UTC (approx).** Previous Cursor session produces a handoff document
  naming the clean deploy source
  `/Users/agoodkind/Sites/tack-deploy-source-20260509T041358Z` and listing 13
  sequential steps for wave 1 execution. The directory name encodes
  `20260509T041358Z`, suggesting it was created at 04:13:58 UTC.
- **~04:14 to 04:27 UTC.** Next agent verifies local `main` is at `dd430c9` and
  is ahead of `origin/main` by 5 unpushed commits. Verifies the clean deploy
  source is missing the audit and repair artifacts that should not be deployed
  (`state_audit_affected_rows.csv`, `state_audit_full_impact.md`,
  `undeclared_props_affected_nodes.csv`, `repair_artifacts/`; all four were
  later deleted from the repo root on 2026-05-10). Confirms the remote source
  tree at `/root/tack` is dirty and still contains those files.
- **~04:27:29 UTC.** `make deploy` is run from the clean source with explicit
  `COMMIT=dd430c9` build args. Backup is written to
  `/root/backups/tack-20260509T042729Z`. The backup script tars FDB volumes
  live, which is noted as relevant to recovery posture (see contributing
  factors). It later turns out that this backup, like every backup since
  2026-04-25, contains zero FDB data files because of the anonymous-volume
  shadowing issue documented in section 1A.
- **~04:28 UTC (approx).** Deploy succeeds. App restarts and reports
  `commit=dd430c9`, `version=main-dd430c9`, `dirty=false`.
- **Shortly after deploy.** Decision is made to run `seed`. The stated
  reasoning is that built-in NodeType metadata in the deployed code changed
  the reference strategy from `direct_slug` to `direct_property`, and the live
  store needs that updated metadata propagated. `migrate` is correctly not run
  because no new SQL migrations were introduced in this wave.
- **Seed completes.** Output includes a production token (redacted in logs
  by convention; not reproduced here). Seed log lines indicate creation of
  what appear to be fresh `org` and `workspace` nodes rather than re-using
  the existing production ones.
- **Backfill preview is run.**
  `./server ops batch backfill.addresses.preview` reports:
  `source_count=9`, `candidate_count=9`, `write_count=7`, `conflict_count=2`,
  `malformed_count=0`, `skipped_count=2`. The two conflicts are `org
  goodkind-io` and `workspace main`. Per the plan's stop condition for
  nonzero conflicts, the apply step is correctly not executed.
- **User reports "tack is fully down."** From their MCP client, normal
  operations no longer work even though processes are healthy.
- **Read-only investigation begins.** A subagent confirms HTTP is up and MCP
  is responding. `tack_get_project` against legacy project IDs fails with
  `unknown reference strategy "direct_slug" for project`. `tack_list_projects`
  with `workspace_reference=main` returns 0 projects. FDB getrange evidence
  shows two parallel orgs and two parallel workspaces:
  - Legacy org `019dc5ad-0408-7e43-9c4d-d3e6736ac058`
  - New org `3dc1c593-35ea-5214-a198-800e9f38916a`
  - Legacy workspace `019dc5ad-0469-71e0-9e73-711bbcc0e93d`
  - New workspace `351ebbfa-3e8b-5ed5-9ae9-65a2eac2ce35`
- **Root-cause hypothesis is formed.** A second subagent confirms with FDB
  getrange evidence that seed checked only the new generic `address_index`
  to determine existence, and that the legacy production org and workspace
  were registered only in the legacy `slug_index`. Seed therefore treated
  them as missing and created new parallel records.
- **Recovery options are evaluated.** Two parallel subagents are spawned: one
  to test whether the live-tar FDB backup at
  `/root/backups/tack-20260509T042729Z` is actually restorable, and one to
  produce a precise forward-fix playbook that retargets the address index
  entries for `goodkind-io` and `main` back at the legacy IDs and reconciles
  any seed-created NodeType / PropertyDef nodes. A third subagent (this one)
  is spawned to draft the retro log in parallel.
- **~05:14 UTC.** Forward-fix vs backup-restore decision is initially
  treated as open pending the parallel subagent results.
- **~05:18 UTC (approx).** Backup-restorability subagent completes and
  reports the empty-backup finding documented in section 1A. Recovery
  options collapse to forward-fix only. A new subagent is spawned to take
  a real `fdbbackup` snapshot before any forward-fix mutation, so the
  forward-fix has an actual safety net.
- **~05:18:02 UTC.** Real-backup subagent begins. Snapshot directory on
  CT 117: `/root/fdb-snapshots/snapshot-20260509T051802Z/`.
- **During real-backup.** Subagent finds no running `backup_agent` in the
  deployment. The first `fdbbackup start -w` invocation stalls because
  the agent is the worker that actually moves data; without one,
  `fdbbackup` registers a backup but never progresses. Subagent brings
  up a `backup_agent` sidecar using the same image, runs the snapshot,
  then stops and removes the sidecar. This is the root cause of why the
  existing script could not have produced a real backup even if it
  pointed at the right volume.
- **~05:35 UTC (approx).** Real-backup completes. fdbbackup reports
  `Restorable: true` at version `1168778944638`. Anonymous data volume
  (`7a90eb88d56c...`) tarred as a belt-and-suspenders companion.
  SHA-256 manifest written. Tarball copied to local machine; checksum
  matches. Cluster health unchanged (`Replication health - Healthy`).
- **Forward-fix playbook subagent completes** at approximately the same
  time, with a 350-line playbook covering 7 phases of remediation.
- **All three preparation subagents complete.** Production has a verified
  real backup for the first time since 2026-04-25. Forward-fix is the
  only viable recovery path. Operator approves autonomous execution
  within an active maintenance window.
- **~05:38:42 UTC.** Execution subagent begins. Brings up
  `backup_agent` sidecar (container `0cfbb0bbde06`) to support
  inter-phase snapshots.
- **05:42:50 to 05:43:30.** Phase 1 rewrites three OLD-org NodeType
  records from `direct_slug` to `direct_property`. Read-modify-write
  with read-back verification on each.
- **05:43:30 to 05:44:00.** Phase 2 clears NEW-org keyspace (9
  clearranges).
- **05:44:00 to 05:44:15.** Phase 3 clears two global `node_resolve`
  rows.
- **05:44:35 (label).** Snapshot after Phase 3 verified `Restorable: true`.
- **05:44:35 to 05:44:50.** Phase 4 clears two stale `address_index`
  rows.
- **05:44:50 to 05:45:10.** Phase 5 SQL `DELETE` on stale
  `org_members` row.
- **05:45:53 to 05:47:09.** Phase 6 backfill apply writes 9 fresh
  `address_index` rows (`written_count=9, conflict_count=0,
  malformed_count=0`).
- **05:47:22 (label).** Snapshot after Phase 6 verified.
- **05:48:17 to 05:48:43.** Phase 7 validation. All MCP probes pass.
- **05:48:43 (label).** Final snapshot verified.
- **05:50:09 UTC.** `backup_agent` sidecar torn down. Production
  containers untouched and healthy.
- **~05:53 UTC.** Independent re-validation. Backfill preview reports
  `source_count=9`, `candidate_count=9`, `write_count=0`,
  `idempotent_count=9`, `conflict_count=0`. All seven production
  containers report healthy uptime unchanged.

---

## 3. Root cause

The seed routine's existence check for the org and workspace appears to have
queried only the new generic `address_index` keyspace. The production org
(`goodkind-io`) and workspace (`main`) were registered only under the legacy
`slug_index`, because they predated the generic address index. Seed therefore
concluded that those entities did not exist and proceeded down the create
path in `ensureNode` (see `cmd/server/seed.go`), generating new deterministic
UUIDs and writing both new NodeValue records and new generic-address index
entries pointing `goodkind-io` and `main` at the new empty IDs. The legacy
records were not deleted; they simply lost their canonical reference and
became unreachable through MCP's reference-resolution path. The planning gap
is that the new seed code did not have a transitional "consult both index
versions" path during the migration window.

---

## 4. Contributing factors

- **Backup script silently fails for FDB.** `scripts/backup.sh` was
  written to tar FDB volumes live, which already carries a consistency
  caveat. The actual outcome is worse than the caveat suggests. The
  script tars the named `tack_fdb-data` volume, but the FDB image's
  `VOLUME /var/fdb/data` declaration causes Docker to mount an anonymous
  volume on top of the named volume's data path. The script therefore
  captures an empty `data/` directory and the database files are
  excluded. This has been the case since the anonymous volume was
  created on 2026-04-25. See section 1A for the full description. The
  immediate effect on this incident is that backup-restore was never a
  real recovery option, but the team only learned that during incident
  response. The broader effect is that production has been running
  without a restorable FDB backup for about two weeks.
- **MCP resolver dedup behavior.** The reference resolver appears to surface
  exactly one match per `(scope, kind, value)` tuple. With the address index
  now pointing at the new empty entities, the resolver consistently returns
  the new IDs and never the legacy ones, which is why MCP looks "wired to a
  fresh tenant" even though the legacy data is still present in FDB.
- **NodeType metadata is per-org rather than global.** Each org has its own
  NodeType records, including the reference-strategy metadata. The legacy
  org still has NodeType records that declare `direct_slug`. The deployed
  binary rejects that strategy, which is what surfaces as `unknown reference
  strategy "direct_slug"` when MCP tools touch the legacy org. A global type
  registry, or a backfill that updates NodeType metadata in place across all
  orgs before the binary changes, would have closed this seam.
- **Deploy / seed / backfill ordering is not a single transaction.** The
  three steps each touch a different layer of state (binary, type metadata
  + bootstrap nodes, address index). Between steps, the system is in a
  transitional shape where some references resolve under the new strategy
  and some still need the old one. There is no single guard rail that holds
  the door shut while these steps complete.
- **Seed has no "transitional" mode.** Seed is idempotent in the steady
  state but does not appear to consult the legacy `slug_index` during the
  migration window. The same seed code that was safe before the address
  refactor and will be safe after it is unsafe in the middle.
- **Preview backfill and seed are not coordinated.** The address backfill
  preview was the right tool to detect that `goodkind-io` and `main`
  already had legacy registrations, and it did detect them as conflicts.
  But preview ran *after* seed, which had already written its own address
  entries. If preview had run before seed, the conflict would have been
  visible against legacy state alone, and seed could have been skipped or
  scoped.
- **Production NodeType metadata can drift from the deployed binary.**
  Because seed propagates NodeType metadata, any deploy that changes
  reference strategy implicitly requires a seed run to keep the live data
  consistent with the binary. That coupling is real but is not enforced
  by the deploy pipeline.

---

## 5. What worked well

- **Clean deploy source process.** The team prepared
  `tack-deploy-source-20260509T041358Z` as a sanitized rsync source that
  excludes audit and repair artifacts. The deployed remote tree did not
  pick up local sensitive files, which matched the wave 1 plan's explicit
  stop condition about not deploying from a tree containing untracked
  artifacts.
- **Explicit build metadata override.** `make deploy` was invoked with
  `COMMIT=dd430c9` so the running binary self-reports the exact commit
  and a clean dirty flag. That made it possible to confirm later that
  the running code matches local `main` and rules out "wrong binary" as
  a hypothesis.
- **Backup taken before deploy (in form, not in substance).** The deploy
  pipeline did invoke `scripts/backup.sh` before any code change touched
  the live processes. The intent was correct. The artifact is empty. The
  pipeline mechanism worked; the script did not. Treat this as a
  what-worked entry only at the level of process discipline, not at the
  level of recovery posture.
- **Read-only investigation discipline.** When the user reported the
  outage, the response sequence was investigate first, mutate second.
  No write operation was issued during diagnosis; FDB getrange was used
  to gather evidence about parallel orgs without changing state.
- **Plan stop condition held.** Backfill preview hit a conflict count of
  2 and the apply step was correctly not run. Without that stop, the
  damage would have extended into the address index for additional
  references.
- **Parallel evidence gathering.** Three subagents (backup test,
  forward-fix playbook, retro log) are running in parallel rather than
  sequentially, which keeps the recovery clock short.

---

## 6. What did not work

- **Seed did not account for legacy data.** The existence check looked
  only at the new index version. There was no fallback to the legacy
  `slug_index` and no opt-in flag to declare "we are mid-migration."
- **No smoke test between deploy, seed, and backfill.** Each step ran to
  completion without an interleaved end-to-end check (for example, an
  MCP `tack_describe_workspace` against the production reference) that
  would have caught the parallel-org state immediately after seed and
  before backfill.
- **Rollback plan was implicit and turned out to be empty.** "We have a
  backup" was the rollback posture going into the deploy. The backup
  test confirmed the backup is empty of FDB data and has been since
  2026-04-25. The rollback path did not exist at the time of the
  incident. This was discovered during incident response, not during
  pre-deploy verification. Section 1A covers this in detail.
- **No deploy-time precheck for legacy data.** The deploy pipeline does
  not inspect the live store for legacy index keys before launching a
  binary that no longer accepts them. A precheck that counted
  `slug_index` entries against `address_index` entries would have
  surfaced the migration gap before it caused user impact.
- **Recovery is contended between two viable paths.** Forward-fix and
  backup-restore both have plausible cases. Without a pre-declared
  preference for one path during a transition deploy, the operator has
  to design the recovery from scratch under time pressure.
- **Jargon in incident communication.** During triage, the operator
  pushed back several times on jargon-heavy explanations of audit
  internals (for example, framing backpressure design options around a
  "gate channel of 64 capacity" rather than describing the user-visible
  behavior, and conflating dropped versus buffered events). The lesson
  is captured separately in
  `/Users/agoodkind/.claude/projects/-Users-agoodkind/memory/feedback_plain_english_over_jargon.md`
  and worth one citation here. Concrete example from this incident: the
  sentence "the gate-of-64 mechanism does not backpressure under low
  concurrency, which is exactly Tack's production shape" landed only
  after rephrasing as "a single producer never blocks because the gate
  has 64 slots free." Future incident write-ups should default to plain
  language with small concrete context, not implementation terms.

---

## 7. Open questions for the retro

- Should `seed` in transition states consult both index versions, and
  what is the explicit cutover signal that lets it stop consulting the
  old one?
- Should backfill have run before seed in this wave, given that backfill
  is the tool that knows how to translate legacy index entries into
  generic ones?
- Should there be a deploy-time precheck for legacy index data when the
  binary's reference-strategy metadata has changed?
- Should NodeType metadata be lifted out of per-org records into a
  global registry, and what would that mean for multi-tenant
  customization?
- How did the empty-backup defect persist for two weeks without any
  smoke test or monitoring catching it, and what is the right minimum
  bar for backup verification going forward (for example, "every backup
  must contain at least one `.sqlite` file or the script exits non-zero")?
- What is the right replacement for the live-tar approach: `fdbbackup`
  driven from a sidecar, the FDB client API, or a structural fix to the
  compose file so the named volume actually holds the data?
- Should `seed` be split into "bootstrap a brand-new org" vs "propagate
  metadata to an existing org," with the latter never creating new node
  records?
- Should MCP have a debug or admin tool to surface "all candidate matches"
  for a reference, so an operator can see when a reference resolves to
  something unexpected without reading FDB directly?
- What is the team's preferred recovery posture during a migration deploy:
  forward-fix by default, restore by default, or document both and decide
  per incident?
- Should the deploy pipeline refuse to deploy a binary whose reference
  strategy differs from the live store's NodeType metadata until a
  declared migration step has run?
- Should the audit ledger have surfaced "two orgs created with seed-style
  actor on the same day" as an alert, given that seed is described as
  idempotent?

---

## 8. Decision points encountered during incident response

Each entry lists the choice the operator faced, the options considered,
what was selected, and the stated rationale. Outcomes will be recorded
once recovery completes.

### 8.1 Run seed after deploy

- **Choice:** Run `/server seed` immediately after wave 1 deploy.
- **Options considered:** (a) run seed to propagate the new NodeType
  metadata, (b) skip seed and rely on backfill, (c) skip seed and defer
  metadata propagation to a separate step.
- **Selected:** Option (a).
- **Rationale at the time:** Built-in NodeType metadata had changed
  from `direct_slug` to `direct_property`, and seed is the documented
  mechanism for propagating that metadata. Seed is also documented as
  idempotent.
- **Outcome:** Seed created parallel org and workspace records.

### 8.2 Skip migrate

- **Choice:** Do not run `/server migrate`.
- **Options considered:** (a) run migrate defensively, (b) skip migrate
  because no new SQL migrations were introduced.
- **Selected:** Option (b).
- **Rationale at the time:** No new SQL migrations exist in this wave;
  running migrate would be a no-op and the project doc explicitly
  prohibits running migrations on HTTP startup.
- **Outcome:** Correct decision; not a contributing factor.

### 8.3 Stop after backfill preview conflicts

- **Choice:** Treat `conflict_count=2` as a stop condition and not run
  apply.
- **Options considered:** (a) override and run apply, (b) stop and
  investigate.
- **Selected:** Option (b).
- **Rationale at the time:** The plan's stop conditions explicitly
  forbid applying backfill if preview reports nonzero conflicts.
- **Outcome:** Prevented the address index from being further
  overwritten.

### 8.4 Read-only investigation before mutation

- **Choice:** Investigate first using FDB getrange and MCP read tools,
  not write tools.
- **Options considered:** (a) attempt a quick fix based on the most
  likely hypothesis, (b) gather evidence with read-only tools first.
- **Selected:** Option (b).
- **Rationale at the time:** The system was in an unknown state and the
  cost of an incorrect mutation was high.
- **Outcome:** Root cause identified with concrete FDB evidence before
  any write.

### 8.5 Forward-fix vs backup restore

- **Choice:** Decide between rewriting the address index entries to
  point at the legacy IDs (and reconciling NodeType records) versus
  restoring `/root/backups/tack-20260509T042729Z` and replaying only
  the safe subset of subsequent operations.
- **Options considered:** (a) forward-fix only, (b) backup restore
  only, (c) test backup restore in isolation, then choose.
- **Selected:** Option (c) initially.
- **Rationale at the time:** Forward-fix preserves any data written
  between deploy and now, but only if the recovery script is correct.
  Backup restore is conceptually simpler but only viable if the live-
  tar snapshot is actually consistent.
- **Outcome:** Backup-restorability test returned a definitive
  not-viable verdict because the backup contains zero FDB data files
  (see section 1A). Recovery options collapsed to forward-fix only.
  The decision became "forward-fix, but take a real backup first so
  the forward-fix has a safety net." Forward-fix completed
  successfully. See section 11.

### 8.6 Take a real backup before forward-fix

- **Choice:** Spawn a separate subagent to take a real `fdbbackup`
  snapshot and a tar of the anonymous data volume, with checksumming
  and an offsite copy, before any forward-fix mutation runs.
- **Options considered:** (a) skip the backup and accept the risk,
  (b) take a quick `fdbbackup` and proceed, (c) take both an
  `fdbbackup` and an anonymous-volume tar with checksums and an
  offsite copy.
- **Selected:** Option (c).
- **Rationale at the time:** The forward-fix touches address-index
  keys and NodeType records. If the script has a bug, it could make
  the production state worse. Without a real backup, there is no
  rollback. The cost of taking a proper backup is small relative to
  the cost of an unrecoverable failure during forward-fix.
- **Outcome:** Real backup created and verified.
  `fdbbackup describe` reports `Restorable: true` at version
  `1168778944638`. Snapshot at
  `/root/fdb-snapshots/snapshot-20260509T051802Z/` on CT 117. Bundled
  tarball SHA-256 matches between CT 117 and the local offsite copy.
  Production cluster health unchanged. The empty-backup defect's root
  cause was identified during this work: the existing script targets
  the wrong volume AND no `backup_agent` is running, so even a corrected
  script could not produce a `fdbbackup`-style consistent backup
  without a long-running agent process.

### 8.6 Spawn retro log in parallel with recovery

- **Choice:** Start the retro document while recovery is still in flight.
- **Options considered:** (a) wait until after recovery, (b) draft now
  and update as the incident resolves.
- **Selected:** Option (b).
- **Rationale at the time:** Memory of decisions and timing is freshest
  during the incident; capturing it now reduces reconstruction error.
- **Outcome:** This document.

---

## 9. Outstanding work

- **Recovery path selection.** Forced to forward-fix because the backup
  is empty (section 1A). Pending the forward-fix playbook draft and
  the real-backup subagent's completion. Both must finish before any
  mutation runs.
- **Take a real backup first.** Run `fdbbackup` against the live
  cluster, tar the anonymous data volume by ID, checksum both, copy
  offsite. This is the safety net for the forward-fix.
- **Recovery execution (forward-fix only).** Rewrite `address_index`
  entries for `goodkind-io` and `main` to point at the legacy IDs.
  Reconcile any seed-created NodeType or PropertyDef nodes. Update
  legacy NodeType reference strategy from `direct_slug` to
  `direct_property` so the deployed binary stops rejecting it. Delete
  the orphaned new org and workspace records, or leave them tombstoned
  with a note. Each mutating step under explicit operator approval.
- **Validation after recovery.** Confirm:
  - `tack_describe_workspace {"workspace_reference":"main"}` returns
    the legacy workspace and the expected project list.
  - `tack_get_project` against legacy IDs no longer errors.
  - `tack_list_projects` returns the production project set.
  - Address index has exactly one entry per production reference.
  - No orphaned org or workspace nodes remain (or they are explicitly
    documented).
- **Resume the original migration plan.** Once the production state is
  back to a single canonical org and workspace, resume wave 1 from the
  backfill step:
  - Re-run `./server ops batch backfill.addresses.preview` and confirm
    `conflict_count=0`.
  - Run apply with `TACK_BACKFILL_APPLY=true`.
  - Validate post-backfill MCP and resolver flows per plan section 5.
  - Continue with the remaining incident-state-repair work and wave 2
    legacy-surface cleanup.
- **Post-success cleanup.**
  - Sanitize and archive the seed token printed during this incident.
  - Add the seed planning gap and the precheck recommendation to the
    plan or an explicit follow-up doc.
  - File follow-up issues for each open question that the team chooses
    to act on.
- **Backup system rebuild (independent of this incident).** The empty-
  backup defect is a separate operational issue with its own urgency.
  Outstanding items:
  - Replace `scripts/backup.sh` for FDB with a real backup mechanism.
  - Verify Yugabyte backup behavior under the same scrutiny.
  - Add a backup-content sanity check that fails CI or alerts if a
    backup tarball does not contain the expected file types.
  - Audit and correct any docs or runbooks that reference the existing
    backup script as a recovery resource.
  - Decide on a backup verification cadence (for example, monthly
    test-restore into a scratch cluster).
- **Retro itself.** Hold the scheduled retro using this document as the
  starting timeline. Update sections 1, 2, 5, 6, 8, and 9 with the
  actual recovery outcome and any newly visible factors.
- **Phase 2 wave 1 monitoring and parity tooling.** Two coding subagents
  (Opus, isolated worktrees) are in flight at the time of this update:
  - **Audit parity tool.** A `./server ops audit parity` Go subcommand
    that compares `audit.events` against `audit.events_v2` during the
    dual-write rollout. Replaces the planned `scripts/audit-parity.sh`,
    which is now banned under the moratorium in section 1G.
  - **Phase 2 wave 1 monitoring.** Producer, consumer, and dual-write
    metrics plus structured logs intended to surface drift in minutes
    rather than days. The motivating counter is the 11-hour silent
    drop window that section 1A and section 11 only caught after the
    fact.
  Status is in_progress; results will be folded into a follow-up
  section once the subagents complete and the output has been
  verified against the running system.

---

## 10. References

### Source documents and code

- Migration plan:
  `/Users/agoodkind/.cursor/plans/finish-slug-and-state_74920a00.plan.md`
- Project documentation: `/Users/agoodkind/Sites/tack/CLAUDE.md`
- Seed entry point: `/Users/agoodkind/Sites/tack/cmd/server/seed.go`
- Seed service logic: `/Users/agoodkind/Sites/tack/internal/service/seed.go`
- MCP server entry point:
  `/Users/agoodkind/Sites/tack/internal/adapters/mcp/server.go`
- MCP reference resolver:
  `/Users/agoodkind/Sites/tack/internal/adapters/mcp/tools/reference_parameters.go`
- Address backfill code:
  - `/Users/agoodkind/Sites/tack/internal/ops/backfill_addresses_preview.go`
  - `/Users/agoodkind/Sites/tack/internal/ops/backfill_addresses_plan.go`
  - `/Users/agoodkind/Sites/tack/internal/ops/backfill_addresses_types.go`
- Reference / generic address contract:
  - `/Users/agoodkind/Sites/tack/internal/domain/node/reference.go`
  - `/Users/agoodkind/Sites/tack/internal/service/node_address.go`
  - `/Users/agoodkind/Sites/tack/internal/adapters/foundationdb/node_address.go`
  - `/Users/agoodkind/Sites/tack/internal/adapters/mcp/tools/resolve.go`

### Prior-incident context (not the cause of this incident)

The 2026-05-08 state-repair incident produced two output directories at the
repo root: `repair_artifacts/2026-05-08-remaining-safe/` and
`repair_artifacts/2026-05-08-final-report-package/`. Both directories were
deleted on 2026-05-10 because the repair work had shipped and the manifests
contained production data. The current-terminology record of that work lives
at `docs/incidents/2026-05-09-seed-parallel-org/reports/state_repair_execution_report.md`.

### Operational artifacts referenced in the timeline

- Clean deploy source:
  `/Users/agoodkind/Sites/tack-deploy-source-20260509T041358Z`
- Live-tar backup taken pre-deploy:
  `/root/backups/tack-20260509T042729Z` (on CT 117)
- Running binary self-reports: `commit=dd430c9`, `version=main-dd430c9`,
  `dirty=false`.
- Backfill preview result on incident day: `source_count=9`,
  `candidate_count=9`, `write_count=7`, `conflict_count=2`,
  `malformed_count=0`, `skipped_count=2`, conflicts on
  `org goodkind-io` and `workspace main`.

### Identifiers seen during diagnosis

- Legacy org: `019dc5ad-0408-7e43-9c4d-d3e6736ac058`
- New parallel org: `3dc1c593-35ea-5214-a198-800e9f38916a`
- Legacy workspace: `019dc5ad-0469-71e0-9e73-711bbcc0e93d`
- New parallel workspace: `351ebbfa-3e8b-5ed5-9ae9-65a2eac2ce35`
- Reference strings affected: `goodkind-io` (org), `main` (workspace).

### Sibling artifacts produced for this incident

- This file:
  `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/retro_log.md`
- Backup restorability test report:
  `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/backup_test_report.md`
  Verdict: not viable. Drives the section 1A finding.
- Forward-fix remediation playbook (from parallel subagent): expected at
  `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/remediation_playbook.md`
  once the draft completes.
- Real backup report (from real-backup subagent): expected at
  `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/fdb_backup_report.md`
  once the snapshot completes.
- Ops consolidation plan (from plan agent):
  `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/ops_consolidation_plan.md`
  (936 lines). Describes the move from shell scripts and `make deploy`
  to `./server ops *` subcommands and an image-based deploy. See
  section 1H.
- Wave 1 runbook verification report (from runbook verification
  subagent): expected at
  `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/wave1_runbook_verification_report.md`
  once that subagent completes.
- Audit parity implementation report (from parity tool subagent):
  expected at
  `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/audit_parity_implementation_report.md`
  once that subagent completes. See the outstanding-work entry in
  section 9.
- Wave 1 monitoring implementation report (from monitoring subagent):
  expected at
  `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/wave1_monitoring_implementation_report.md`
  once that subagent completes. See the outstanding-work entry in
  section 9.

### Sensitive-data note

The seed run printed a production-mode API token to stdout during the
incident. That token is not reproduced in this document. It should be
rotated as part of post-recovery cleanup, and any operational logs that
captured it should be redacted or rotated alongside the token itself.

---

## 11. Resolution

The forward-fix completed successfully on 2026-05-09 between 05:38:42
UTC and 05:50:09 UTC. Wall-clock duration including the `backup_agent`
sidecar lifecycle was about 12 minutes.

### Mutations applied

- 3 NodeType JSON rewrites under OLD org. Strategy changed from
  `direct_slug` to `direct_property` for the org, workspace, and
  project type definitions. Read-back verification confirmed each.
- 9 FDB clearranges removed all NEW-org keyspace under
  `3dc1c593-35ea-5214-a198-800e9f38916a`.
- 2 global `node_resolve` rows cleared.
- 2 stale `address_index` rows cleared.
- 1 `org_members` SQL row deleted.
- 9 fresh `address_index` rows written by `backfill.addresses.apply`.

Total: 17 FDB mutations plus 1 SQL DELETE plus 1 batch op (which wrote
9 keys). Effective scope was about 26 FDB key writes/clears plus 1 SQL
DELETE, well under the playbook's 45-key estimate.

### Snapshots taken during execution

Three `fdbbackup` snapshots, each verified `Restorable: true` against
the actual backup subdirectory (after the URL extraction fix described
below in Anomalies):

- After Phase 3 (post-cleanup, pre-address-rewrite):
  `snapshot-after-phase3-20260509T054254Z.tar.gz`,
  SHA-256 `4324c382ea9b8d64f74dd6f077c2e29d521e6d7b07305fad876ae54a0b82641a`.
- After Phase 6 (post-backfill apply):
  `snapshot-after-phase6-20260509T054722Z.tar.gz`,
  SHA-256 `31459a3801517037d3b209501530877c0e9caab253cc6d05d0ca360c1f5fa99f`.
- Final (post-validation):
  `snapshot-final-20260509T054843Z.tar.gz`,
  SHA-256 `b814ac1dfd66646b8b90e9a591db8e087cfda3251387edfc4f40ae4f499b0103`.

All three are bit-identical between CT 117 and the operator's local
machine.

### Validation results

All Phase 7 MCP probes passed against the running production binary:

- `tack_describe_workspace` for `main` returns OLD workspace UUID
  `019dc5ad-0469-71e0-9e73-711bbcc0e93d` with reference strategies
  rendering as `direct_property:slug` for org/workspace and
  `direct_property:identifier` for project.
- `tack_list_projects` returns 7 projects (TACK, MWAN, LAB, OSS, APP,
  WEBSITE, CLYDE).
- `tack_get_project` for TACK, MWAN, and APP all resolve successfully.
  No `unknown reference strategy` errors.
- `org_members` has one row pointing at OLD org with role 20.

### Independent re-validation (operator-side)

After the execution subagent completed, an additional read-only check
ran from outside the execution subagent context:

- `backfill.addresses.preview` reports `source_count=9`,
  `candidate_count=9`, `write_count=0`, `idempotent_count=9`,
  `conflict_count=0`, `malformed_count=0`. The system is in steady state
  for the address layer.
- All seven production containers (`tack-app-1`, `tack-yugabyte-1`,
  `tack-meilisearch-1`, `tack-fdb-1`, `tack-temporal-ui-1`,
  `tack-temporal-1`, `tack-temporal-db-1`) report healthy uptime
  unchanged from before the maintenance window. No restarts, no
  recreations.

### Anomalies during execution

1. **Snapshot URL extraction.** The first `fdbbackup describe`
   invocation pointed at the parent path inside the snapshot mount and
   reported `Restorable: false`. The actual backup landed in a
   timestamped subdirectory inside that parent. Corrected by extracting
   the real path from `fdbbackup status` output. Phase 3 manifest and
   tarball were rebuilt with the corrected describe.txt before the
   operation continued.
2. **`agent-gate` precommit hook over-matched** on benign assignment
   prefixes in shell commands. No mutating commands were skipped. The
   subagent worked around by avoiding shell variable assignment at the
   start of command lines.
3. **Default `backup_agent` entrypoint exits immediately.** The first
   `docker run` of the FDB image with `backup_agent --log` exited within
   seconds. Switching to `--entrypoint /usr/bin/backup_agent` with an
   explicit `-C /etc/foundationdb/fdb.cluster` made the agent start and
   stay up. Worth noting for future ad-hoc backup runs and for any
   future persistent backup_agent service in the Compose stack.

### Files produced during resolution

- Execution report:
  `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/execution_report.md`
- Snapshot manifest:
  `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/execution_snapshots_manifest.txt`
- Status one-liner:
  `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/execution_status.txt`
- Three bundled tarballs in the same directory.
- `/root/snapshot.sh` on CT 117 (now reusable for ad-hoc snapshots).
- `/tmp/strategy_rewrite.py` locally (the read-modify-write utility for
  Phase 1 rewrites).

### Operator follow-ups identified by execution subagent

1. Re-run `./server seed` once per the address-index steady state to
   confirm idempotence under the rewritten NodeType records. Expected:
   short-circuit on `address_index` lookups, no parallel records
   created.
2. Audit `/root/backups/` and replace `scripts/backup.sh`. Today's
   incident proved the existing script is non-functional. Use
   `fdbbackup` with a sidecar or a persistent `backup_agent` Compose
   service. Reference the real-backup approach used during this
   incident.
3. Plan a future change to remove the legacy `slug_index` keyspace.
   Today it is left intact because `backfill.addresses.apply` reads
   from it. Once the codebase no longer references
   `legacySlugIndexKeyFamily`, the remaining 9 rows can be cleared.
4. Add a regression test that exercises the seed flow against an FDB
   instance whose `address_index` already has entries, asserting that
   no parallel org or workspace is created. This is the test that would
   have caught today's planning gap.
5. Commit `/root/snapshot.sh` into the repo under `scripts/` (with the
   describe-URL extraction logic) so the technique is repeatable and
   versioned.

### Incident state

User-visible MCP behavior is restored. The forward-fix preserved all
production data under the legacy org and workspace UUIDs. The forward-
fix did not delete or rewrite any OLD-org data, only the NEW-org
records that seed had created earlier in the incident. Production is
operating on the same UUIDs that existed before today's seed run. The
deployed binary at commit `dd430c9` continues to serve all production
traffic.
