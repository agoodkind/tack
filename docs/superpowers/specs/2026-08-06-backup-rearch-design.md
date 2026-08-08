# Backup and resilience: no special guest

## Terms

- **Hypervisor**: the physical Proxmox host (vault). It runs every guest.
- **Guest**: one LXC container on the hypervisor, with its own guest id,
  IPv6 address, and Docker daemon. Guest is the unit of failure this design
  protects against.
- **Service container**: one Docker container inside a guest's compose
  stack. Every database node, broker, and app instance in this document is a
  service container, placed one per guest for its tier.

This document never uses the word machine. Where an engine's documentation
says machine (FoundationDB counts fault domains in machines), the unit here
is the guest.

## The contract

Losing any one guest must not lose data or stop the service. Five properties
define done:

- Durable. Every backup survives the loss of any guest and of the hypervisor,
  because it lives in the off-host object store.
- Scalable. No backup step reads the dataset row by row.
- Continuous. Protection advances on its own; no step depends on a person
  remembering to run it.
- Distributed. Every stateful service runs on more than one guest.
- Recoverable. Restores are rehearsed on a schedule, so a backup that cannot
  restore is discovered by the rehearsal, not by a disaster.

Two numbers bound the design. A disaster may lose at most the last few seconds
of writes. Loss of any single guest heals automatically in seconds.

One structural rule: backup work never runs on a guest that is serving
traffic. Snapshot creation by a database is exempt because it links files
instead of copying them and is near-free.

## Target topology

Five guests carry the service.

Three data guests each run one member of each data service: a YugabyteDB node
(the SQL database holding auth and the audit ledger), a FoundationDB process
(the store holding all product data), and a Kafka broker (the queue carrying
audit events). Each service keeps its data on all three guests, so any one
guest can die and all three services keep serving.

Two app guests each run the stateless app server. The proxy (traefik) probes
both with an active health check and routes only to a live one.

Guests reach each other by IPv6 address, rendered from `service_mapping.yml`,
the single source of truth for guest addresses. Per-guest DNS names are not
used: the public wildcard record sends every name to the proxy, so a name for
a non-proxied guest would resolve to the wrong place.

QA mirrors the full topology on the testbed. Testbed guest ids are production
plus 100. Every phase lands on QA before production.

## The ledger database (YugabyteDB)

Three nodes hold three copies of every row. When the node leading a piece of
data dies, the other two elect a new leader within about 3 seconds and writes
continue. This is automatic; no operator acts.

- Growing from one node to three is an online operation: the second and third
  nodes join the first, and the copy count rises to three when the third
  joins. Join the second and third back to back, because the intermediate
  two-node state has a fragile coordination quorum.
- Each node declares a distinct location so the cluster can express fault
  tolerance, mounts the same patched startup script the single node uses
  today, and carries explicit memory limits. Explicit limits replace the
  default self-sizing, which lets the database claim most of a guest's
  memory and starve everything else.
- The app connects with a connection string listing all three nodes. The
  existing database driver supports this with no code change. Callers keep
  their own retry, because an in-flight query on a dying node still fails.

## The product store (FoundationDB)

Three service containers, one per data guest, hold two copies of every key
(the mode called double). Double fits three guests; the three-copy mode needs
three live guests and would turn one guest loss into an outage. Three
coordinators, one per data guest, keep the cluster's shared configuration.

Three defects in the current setup must be fixed before any expansion:

1. Every client mounts the cluster file read-only. The cluster updates that
   file when coordinators change, so clients need write access or they
   silently keep stale coordinators.
2. The startup script rewrites the cluster file to a single entry on every
   start. It must preserve the real coordinator list.
3. A new guest's process started with the current script creates its own
   separate cluster. New guests must be handed the existing coordinator
   list.

The cluster file addresses coordinators by the data guests' pinned IPv6
addresses from `service_mapping.yml`, with each guest publishing its
FoundationDB port. Guest addresses are stable; container addresses are not
and are never persisted. (The pinned version also supports DNS names in the
cluster file, resolving IPv6-first, as a fallback if literals prove awkward.) Client transactions currently retry forever with no timeout; a
deliberate timeout is set before expansion. Each data guest runs one backup
agent; agents cooperate automatically.

## The event queue (Kafka)

Three brokers hold three copies of the audit event topic, with writes
requiring two live copies. One broker can die without losing accepted events.

- The cluster's membership record was created in the static style, and no
  documented online path grows a static membership from one to three. Two
  candidate routes exist: upgrade the cluster to dynamic membership and add
  brokers, or rebuild the membership as three static members in a
  maintenance window. The route is chosen by proving one on QA first.
- Raising the copy count on the existing 256-partition topic, and on the
  topic that stores consumer positions, is an online, bandwidth-throttled
  data movement.
- Each broker advertises its own routable address. Clients need only a longer
  address list; the producer already requires acknowledgment from all copies.
- The audit producer's delivery budget rises above 14 seconds, because a
  hard broker loss costs up to 9 seconds of lease expiry plus up to 5 seconds
  of client refresh before recovery, and the current 10-second budget can
  expire inside that window.

## The app tier

The app holds no per-client state, so any instance can serve any request;
this is verified in code, and write dedup lives inside the product store's
transactions where it works across instances. Two fixes precede a second
instance:

1. The app gains a health endpoint that checks its datastores. The proxy's
   `tack-service` entry lists both instances and probes that endpoint.
2. The ledger signer (the notarizer, which signs the audit chain every
   minute) runs only in the audit-consumer process, never in the app. One
   signer identity exists: a single vault-stored key in the configs repo,
   rendered at deploy time instead of generated per host, rotated as one
   operation, with the signing-key id recorded per signature (the schema
   already does) and the signing hostname recorded in the notarization row
   for forensics. Per-guest keys are rejected: they would make verification
   depend on a registry of ephemeral guest identities and would recreate the
   multiple-identities-on-one-chain ambiguity this fix removes.

The compose file pins development-mode auth, which accepts any UUID as a
login. A second instance behind public ingress raises the urgency of the
existing fix ticket (TACK-261).

## Derived stores

The search index (Meilisearch) and the analytics projection (ClickHouse)
rebuild from their sources of record and are never backed up. Temporal
workflow state has no backup, matching the recovery runbook.

## Backups on this topology

- Product data streams continuously to the object store, restorable to any
  point in the stream's window. Unchanged in shape; one agent per data guest.
- The ledger exports an engine-native snapshot to the object store on a
  schedule, driven from a non-leading node. The export's archive phase
  collects snapshot files from all three nodes according to which node leads
  each piece of data; the current implementation archives from a single service container and is
  invalid at three nodes, so it is rewritten. The in-cluster point-in-time rewind schedule
  remains as the protection against corruption.
- The bare `ops backup` command runs nothing; it prints its subcommands and
  exits nonzero. The row-by-row database dump, the workflow-database dump,
  the search-index archive, their manifest and verify machinery, and the
  unused audit-archive bucket are deleted. The recovery runbook consumes
  none of their artifacts.

## Alarms

Each mechanism reports one number: seconds since its last success. An alert
fires when that number ages past its threshold. Covered: the product-data
stream's restorable point, the ledger's last completed export, the last
passing restore rehearsal, and each cluster's under-replication state. Alarms
fire on staleness rather than on failure, because a silently broken backup
never reports a failure.

## Rehearsals

A restore rehearsal runs on a schedule against the exported artifacts, in
throwaway containers, never against production. A failover rehearsal kills
one data guest on QA, verifies every service converges and the app serves
throughout, then restores the guest. Both rehearsals feed the staleness
alarms.

## Provisioning (configs repo)

The configs repo provisions one guest per hand-written resource; this work
adds the multi-guest pattern:

- One `service_mapping.yml` entry per new guest, plus testbed counterparts
  at id plus 100, plus a parent group for the cluster axis.
- One OpenTofu resource per guest, each with its own MAC address, its own
  Docker IPv6 /96, and a matching neighbor-discovery proxy prefix.
- Per-guest identity (node ids, seed-versus-join role, which single guest
  may initialize the product store or run migrations) lives in `host_vars`
  files, one per guest.
- The deploy playbook splits: host preparation (Docker, IPv6 forwarding,
  neighbor proxy) runs on every guest; one-time steps (provisioning,
  migrations, seeding) run on exactly one.
- Every guest runs a daily Docker prune as its own systemd service and
  timer pair, removing unused images and build cache older than seven days.
  Image accumulation with no reclaim is the same disk-fill mechanism that
  killed the databases in July; the prune never touches volumes.
- Every loopback and compose-internal address in the rendered environment
  becomes a routable address from the mapping: the ops database URL, the
  audit database strings, the Kafka broker list, and the ClickHouse string.
- Before sizing is committed, read the hypervisor's real free capacity. The
  repo declares about 64 GB already committed across guests and does not
  record the physical ceiling. If the reading comes up short, the unmanaged
  developer sandbox guest (debianct: 8 cores, 16 GB) is approved for
  reclaim; its destruction is confirmed with the operator first.

## Rollout phases

Every phase lands on QA first.

1. No new guests: delete the dump machinery and guard the bare command, set
   explicit database memory limits, add the app health endpoint, single-home
   the ledger signer in the audit-consumer, raise the producer delivery
   budget, and set per-service memory limits on the existing guest.
2. Provisioning groundwork: mapping entries, guests, host_vars, the deploy
   split, per-guest networking.
3. Ledger database to three nodes: joins, the multi-host connection string,
   the export rewrite, the scheduled follower-driven export, and the
   under-replication and staleness alarms.
4. Product store to three guests: fix the three cluster-file defects, join
   the new guests' processes, switch to double, DNS-named coordinators,
   one backup agent per data guest, and the client timeout decision.
5. Event queue to three brokers: the membership route proven on QA, brokers
   joined, topic copies raised, two-copy write requirement set, per-broker
   advertised addresses.
6. Second app instance behind the health-checked proxy.
7. Scheduled rehearsals and the complete alarm set.

## Interactions

- TACK-336, the durable dead-letter queue for audit events plus the queue
  retention raise, proceeds independently and complements phase 5.
- The audit cold archive design (columnar files in the object store) is
  unblocked by nothing here.
- TACK-261, replacing development-mode auth, rises in urgency with phase 6.
