# Backup and resilience across guests

## Terms

A hypervisor is the physical Proxmox host, vault. A guest is one LXC container
on the hypervisor with its own guest ID, IPv6 address, and Docker daemon. This
design protects against guest failure. A service container runs inside a guest's
compose stack. Each database node, broker, and app instance runs in one service
container, with one container per guest for each tier.

FoundationDB uses the term machine for a fault domain. This design uses the
guest as that fault domain.

## Contract

The service must preserve data and continue serving after any one guest fails.

- Store durable backups in the off-host object store to survive guest and
  hypervisor failure.
- Use backup operations that do not read the dataset row by row.
- Advance protection automatically rather than requiring manual steps.
- Run each stateful service on more than one guest.
- Rehearse restores on a schedule to detect unusable backups before a disaster.

A disaster may lose at most the last few seconds of writes. The service must
recover automatically from one guest's failure within seconds.

Run backup work on guests that do not serve traffic. Database snapshot creation
may run on serving guests because it links files rather than copying them.

## Target topology and addresses

Three data guests each run a YugabyteDB node for auth and the audit ledger,
a FoundationDB process for product data, and a Kafka broker for audit events.
Each service replicates data across these guests and tolerates one guest failure.
Two app guests run stateless servers. Traefik probes both and routes only to
live instances.

Guests connect by IPv6 addresses from `service_mapping.yml`. Do not use
per-guest public DNS names. The public wildcard resolves every name to the
proxy, including names for guests that the proxy does not serve.

Render hosts-file entries in every container that map permanent ledger node
identities to their addresses. Do not use public DNS for those identities.
The ledger engine persists each node's announced identity. An address used as
that identity becomes stale after renumbering.

QA mirrors the topology on the testbed. Testbed guest IDs equal production IDs
plus 100. Validate every phase on QA before production.

## Storage placement

Use the hypervisor's existing drives without adding a third drive.

- Place the three ledger guest roots, including write-ahead logs and tablet
  data, product-store data, and app guests on NVMe.
- Place asynchronous, non-critical IO on the SATA SSD with DRAM cache. This
  includes the object-store guest, owner's throwaway drill engines and export
  staging, and hypervisor guest backups. Place broker data there only after
  requests stop waiting on the broker.
- Prohibit unthrottled bulk copies on the ledger's pool. A bulk write can stall
  its write-ahead log and leader, triggering an election.

## Ledger database: YugabyteDB

Three nodes store three copies of every row. When a leader fails, the other two
elect a replacement within about 3 seconds and resume writes without operator
intervention.

- Join the second and third nodes online, back to back. The cluster achieves
  three copies after the third joins. The intermediate two-node coordination
  quorum cannot tolerate a node failure.
- Require each node to announce a permanent name rather than an address. Render
  hosts entries on every Tack guest from inventory. Renumbering changes one
  inventory line and requires redeployment, not a stored identity change.
- Rename only in a three-node cluster. Retire one node under its old name,
  rejoin it under the new name, and let the surviving two replicate its data
  onto it before renaming another. Do not edit the catalog. A single-node
  cluster cannot rename a node; restore exported rows into a fresh node to
  recover from a stale identity.
- Declare distinct node locations for fault tolerance. Mount the existing
  patched startup script and set explicit memory limits rather than letting
  the database size itself from most of the guest's memory.
- List all three nodes in the app connection string. The existing driver
  supports this without a code change. Retain caller retries because an
  in-flight query still fails when its node dies.

## Product store: FoundationDB

Three service containers, one per data guest, store two copies of every key in
redundancy mode double. Double tolerates one failure among three guests. The
three-copy mode requires three live guests and cannot tolerate that failure.
Run three coordinators, one per data guest, for shared cluster configuration.

Before expansion, correct these defects:

1. Give clients write access to the cluster file. Read-only mounts prevent
   clients from saving coordinator changes.
2. Preserve the current coordinator list on startup rather than rewriting the
   cluster file to a single entry.
3. Provide the existing coordinator list before starting a new guest's process
   rather than allowing it to create a separate cluster.

Use the data guests' pinned IPv6 addresses from `service_mapping.yml` in the
cluster file and publish each guest's FoundationDB port. Do not persist
container addresses. The pinned version also supports DNS coordinator names
with IPv6-first resolution if literal addresses prove impractical. Set a client
transaction timeout before expansion; transactions currently retry indefinitely.
Run one backup agent per data guest. The agents cooperate automatically.

## Event queue: Kafka

Three brokers store three copies of the audit event topic. Require two live
copies for writes to tolerate one broker failure without losing accepted events.

- Prove a membership expansion method on QA. The cluster uses static
  membership and does not have a documented online expansion from one to three.
  Either upgrade to dynamic membership and add brokers or rebuild three static
  members during a maintenance window.
- Increase copies of the existing 256-partition audit topic and
  consumer-position topic online with bandwidth throttling.
- Advertise each broker's routable address. Extend client address lists.
  The producer already requires acknowledgment from all copies.
- Raise the producer delivery budget above 14 seconds. Broker failure can
  require 9 seconds of lease expiry and 5 seconds of client refresh. The current
  10-second budget can expire before recovery.

## App tier

The app does not store per-client state. Any instance can serve any request.
Product-store transactions deduplicate writes across instances. Before adding
the second instance, add datastore checks at the app health endpoint and list
both instances in Traefik's `tack-service` health checks.

Run the ledger notarizer, which signs the audit chain every minute, only in the
audit-consumer process. Each guest generates its own key. Record the signing
host on each notarization row, store accepted signer identifiers per environment
in the configs vault, and reject signatures outside that set.

Do not share one environment key as proposed on 2026-08-08. A compromised key
would affect every guest. Without an accepted signer set, an attacker with app
guest access could generate a key and forge checkpoints under an identifier
that verification could not distinguish from a legitimate new guest. The
accepted set permits revocation of individual guest keys.

The compose file pins development-mode auth, which accepts any UUID as a login.
A second instance behind public ingress increases the urgency of TACK-261.

## Request processing

Remove synchronous ledger and broker writes from authenticated reads before data
replication expands. The ledger stores both auth and compliance records;
requests that wait on its writes also wait on replica acknowledgments.

Each authenticated read must perform zero ledger writes, at most one ledger
read, and zero synchronous broker round trips.

- Update exact token last-use times asynchronously.
- Read membership at most once per request and cache it per principal.
  Invalidate the cache when membership changes.
- Cache token lookups by token hash for a short lifetime and invalidate on revoke.
- Buffer read-class and auth-accepted audit events in the app and submit batches.
  Continue committing state-change events in the product-store transaction.

## Derived stores and backups

Rebuild the search index from authoritative data rather than backing it up.
Do not back up Temporal workflow state, as the recovery runbook specifies.

Stream product data continuously to the object store for restoration within
the stream's time window, using one backup agent per data guest.

Schedule engine-native ledger snapshot exports from a non-leading node.
Collect snapshot files from all three nodes according to tablet leadership.
Rewrite the current single-container archive implementation before using three
nodes. Retain the in-cluster point-in-time rewind schedule for corruption.

Make bare `ops backup` print subcommands and exit nonzero without running work.
Delete row-by-row database dumps, workflow-database dumps, search-index archives,
their manifests and verification machinery, and the unused audit-archive bucket.
The recovery runbook does not use those artifacts.

## Alarms and rehearsals

Report seconds since last success for the product-data restorable point, ledger
export, restore rehearsal, and each cluster's under-replication state. Alarm when
that age exceeds its threshold. A silently stopped job may never report failure.

Schedule restore rehearsals from exported artifacts in throwaway containers,
never production. On QA, kill one data guest, verify that all services recover
and the app continues serving, then restore the guest. Update the success
metrics after passing rehearsals.

## Provisioning in configs

Ansible manages each deployment field until a provider adopts it.

- Add a mapping entry and OpenTofu resource per guest and testbed counterpart
  at ID plus 100. Add a parent cluster group.
- Assign each guest a MAC address, Docker IPv6 /96, and matching
  neighbor-discovery proxy prefix.
- Store node IDs, seed or join roles, and the sole provisioning and migration
  owner in one `host_vars` file per guest.
- Run host preparation on every guest. Run provisioning, migrations, and
  seeding on exactly one guest.
- Install a daily systemd Docker prune service and timer on every guest.
  Remove stopped containers, unused networks, unreferenced images, and build
  cache older than seven days. Do not remove volumes. Unreclaimed images filled
  database disks in July.
- Replace loopback and compose-internal addresses in the rendered ops database
  URL, audit connection strings, and Kafka list with mapping addresses.
- Measure free hypervisor capacity before committing sizes. The repository
  commits about 64 GB across guests but does not record physical capacity.
  If capacity falls short, request operator confirmation to destroy the approved
  unmanaged `debianct` sandbox with 8 cores and 16 GB.

## Rollout order

Validate each phase on QA first. Complete storage placement and phase 8 before
phases 4 and 5, which add replica writes and three copies of product-store and
broker IO to the ledger's drive.

1. Phase 1: Guard bare backup, delete dump machinery, set database and service
   memory limits, add app health checks, run the signer only in audit-consumer,
   and raise the producer budget without adding guests.
2. Phase 2: Add mappings, guests, `host_vars`, deployment separation, and guest
   networking.
3. Phase 3: Expand the ledger to three nodes, extend connection strings, rewrite
   export, schedule follower exports, and add replication and staleness alarms.
4. Phase 8: Make last-use updates asynchronous, cache tokens and memberships,
   limit membership reads to one, and batch read-class events.
5. Phase 4: Correct cluster-file handling, expand product storage to three guests
   in double mode, use DNS-named coordinators, add backup agents, and set timeout policy.
6. Phase 5: Expand broker membership using the QA-proven method, increase topic
   copies, require two live copies for writes, and set per-broker advertised addresses.
7. Phase 6: Add the second app instance behind the health-checked proxy.
8. Phase 7: Schedule rehearsals and complete alarm coverage.

TACK-336 independently adds a durable dead-letter queue and longer retention,
complementing phase 5. This work does not implement the columnar audit cold
archive. Phase 6 increases the urgency of TACK-261, which replaces development-mode auth.
