# Backup and resilience rearchitecture: no special guest

## Contract

Five properties bind every mechanism: durable (every backup artifact survives
loss of any machine), scalable (no step reads the dataset row by row),
continuous (protection advances with no operator action), distributed (every
stateful tier runs on more than one guest), and recoverable (restores are
rehearsed on a schedule, not assumed). Two numbers: a disaster may lose at most
the last few seconds of writes, and loss of any single guest must heal
automatically in seconds.

The structural rule: no tier lives on exactly one guest, and backup work never
runs on a guest that is serving traffic. Today one guest holds the app and
every data store; that concentration is the vulnerability this design removes.

## Target topology

Five production guests replace the single tack guest:

- Three **data guests**, each running one node of each data cluster: a
  YugabyteDB node (auth and audit ledger), a FoundationDB process (product
  data), and a Kafka broker (audit event transport). Any one data guest can
  die and all three clusters keep serving.
- Two **app guests**, each running the stateless app server and one
  audit-consumer candidate (see Notarizer below). Traefik health-checks both
  and routes around a dead one.

Guests address each other by IPv6 literals rendered from
`service_mapping.yml`, the repo's single source of truth for guest addresses.
The public wildcard DNS points every name at the proxy, so per-guest DNS names
would resolve to the wrong place; literals are the established idiom.
QA mirrors the full topology on the testbed (guest ids are production plus
100) and every phase lands there first.

## Per-tier design, grounded in the lane reports

### YugabyteDB: three nodes, replication factor 3

Joining a third node raises replication to 3 automatically and online
(verified in the pinned `yugabyted` overlay source). Each node gets a distinct
`--cloud_location` so fault tolerance is expressible, the patched overlay
mounted, and explicit memory bounds (`memory_limit_hard_bytes`,
`db_block_cache_size_bytes`) replacing the self-sizing default that caused the
2026-08-05 incident. Node-loss detection is 3 seconds by default. Join nodes
two and three back to back: the two-node intermediate state has a fragile
master quorum. The app keeps plain pgx with a three-host connection string
(zero code change); the smart-driver swap is a later option. Callers need
application-level retry for the 3-second election window plus the 15-second
dead-connection timeout.

### FoundationDB: three machines, redundancy double

Double, not triple: triple needs three live machines, making one loss an
outage. Three coordinators, one per data guest, addressed by DNS name in the
cluster file (supported and IPv6-preferring at the pinned version) to survive
container address churn. Three repo defects must fix first: clients mount the
cluster file read-only while coordinator changes require write access; the
startup overlay rewrites the cluster file to a single entry on every start;
and new machines bootstrap as separate clusters unless handed the existing
coordinators. Migration is online: join machines, `configure double`, then
`coordinators auto`. Client transactions default to unlimited retries with no
timeout; set a deliberate timeout before going multi-machine. One backup agent
per machine; the continuous backup session itself is unchanged.

### Kafka: three brokers, quorum path decided on QA

The current single-node controller quorum was formatted static, and growing a
static quorum has no documented online path. Two candidate routes, both to be
proven on the QA cluster before production: upgrade the cluster to the
dynamic-quorum feature level and add controllers (requires formatting new
nodes outside the stock image entrypoint), or rebuild the quorum as three
static voters during a maintenance window. Either way: raise the existing
256-partition topic and the consumer-offsets topic to replication factor 3 via
the online, throttleable reassignment tool; set minimum in-sync replicas to 2;
give each broker its own advertised routable address. Raise the audit
producer's 10-second delivery budget: a hard broker loss costs up to 9 seconds
of lease expiry plus 5 seconds of client metadata refresh before recovery.

### App tier: two instances behind traefik

The app is verified stateless (explicit stateless mode, no session state, no
sticky requirement; write dedup lives in FoundationDB transactions and works
cross-instance). Two blockers before a second instance:

1. The app has no health endpoint. Add one that checks datastore
   reachability; traefik's `tack-service` gains both upstreams and an active
   `healthCheck` on it.
2. The app process runs an audit notarizer with no leader election, and the
   signing key is generated per host, so two app guests would sign the ledger
   under different identities. The notarizer runs only in the audit-consumer;
   the app's is removed. The signing key becomes a managed secret shared by
   whichever single process signs.

Recorded, out of scope here: the compose file hardcodes development-mode auth
(any UUID is accepted as a bearer); multi-instance behind ingress raises its
urgency (existing ticket TACK-261).

### Derived stores

Meilisearch (search) and ClickHouse (audit analytics projection) rebuild from
their sources of record and are never backed up. Temporal workflow state has
no backup, matching the recovery runbook. Their dump steps are deleted.

## Backup, on the new topology

- **Product data**: the FoundationDB continuous stream to the object store,
  unchanged in shape, now drained by one agent per data guest.
- **Ledger**: the engine-native distributed snapshot export runs on a
  schedule, driven from a follower node, never the leaders' serving path.
  The export's archive phase must collect tablet snapshot files from all
  three nodes keyed on tablet leadership; the current single-container
  implementation is invalid at three nodes and is rewritten. The in-cluster
  point-in-time rewind schedule stays as the corruption layer.
- **Deletions**: the bare `ops backup` command stops running a full snapshot
  (prints subcommands, exits nonzero). Deleted: the row-by-row database dump,
  the temporal-db dump, the meilisearch volume tar, their manifest and verify
  machinery, and the unused audit-archive bucket. The recovery runbook
  consumes none of their artifacts.
- **Alarms**: one staleness metric per mechanism (time since last success:
  stream restorable point, last export, last passing rehearsal, plus cluster
  under-replication). Alert on staleness, never only on failure; silent
  failures do not fail.
- **Rehearsals**: the restore drill runs on a schedule against the exports,
  rewritten for the multi-node artifact shape. A failover rehearsal (kill one
  guest, verify all tiers converge and the app serves throughout, restore the
  guest) runs on QA before rollout and periodically after.

## Provisioning (configs repo)

The repo has no multi-guest service pattern; this establishes one:

- Five new mapping entries (three data guests, one additional app guest, QA
  counterparts at plus 100), each minting its own inventory group; a new
  cluster-axis parent group alongside the existing environment-axis group.
- One OpenTofu resource per guest (or the repo's first `for_each`), each with
  its own MAC, `prevent_destroy`, and a distinct Docker IPv6 /96 plus
  matching NDP proxy prefix.
- Per-guest identity (node ids, seed-versus-join role, which single guest may
  initialize FoundationDB or run migrations) lives in `host_vars`, a nearly
  unused directory that this work turns into the pattern.
- `deploy-tack.yml` splits: shared host preparation (Docker, IPv6, ndppd)
  runs everywhere; single-run steps (`ops provision`, migrations, seeding)
  gate to exactly one host.
- Every loopback and compose-internal address in the rendered environment
  (`tack_ops_database_url`, the audit DSNs, the Kafka broker list, the
  ClickHouse DSN) becomes a routable literal from the mapping.
- Gate before committing sizing: verify vault's physical headroom on the live
  host; the repo-declared guest allocations already sum to about 64 GB, the
  ceiling is not recorded in the repo, and the unmanaged developer sandbox
  guest is the reclaim candidate.

## Rollout phases, QA first at every step

1. Immediate, no new guests: the deletions and bare-command guard, explicit
   database memory bounds, the app health endpoint, notarizer single-homed in
   the consumer, the producer delivery-budget raise, and the per-service
   memory limits on the existing guest.
2. Provisioning groundwork: mapping entries, guests, host_vars, the
   deploy-tack split, per-guest networking.
3. Database spread: three-node join, connection-string change, export
   fan-out rewrite, scheduled follower-driven export, under-replication and
   staleness alarms.
4. FoundationDB spread: fix the three cluster-file defects, join machines,
   `configure double`, DNS-named coordinators, per-machine backup agents,
   client timeout decision.
5. Kafka spread: quorum route proven on QA, brokers joined, topic
   replication raised, min in-sync 2, per-broker advertised addresses.
6. Second app instance behind traefik with active health checks.
7. Scheduled rehearsals (restore drill and guest-kill failover drill) plus
   the full alarm set.

## Interactions

- TACK-336 (durable dead-letter queue, Kafka retention raise) proceeds
  independently and complements phase 5.
- The audit cold archive (Iceberg on the object store, per the horizontal
  design doc) is unblocked by nothing here.
- TACK-261 (development-mode auth in production) rises in urgency with
  phase 6.
