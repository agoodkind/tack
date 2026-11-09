# Resilience constraints

The guest is the failure unit for distributed services. A guest is one LXC
container with its own network identity and Docker daemon. A service container
runs inside that guest. The hypervisor runs the guests; several guests on one
hypervisor do not provide protection against hypervisor loss.

## Recovery contract

Loss of one guest must not lose acknowledged data or stop the service beyond
the accepted recovery bounds. Backup artifacts must survive guest and hypervisor
loss in off-host object storage. Backup capture must use engine-native snapshots.
The latest restorable point must advance automatically. Backups must pass
scheduled restoration checks. Stateful services must distribute replicas across
separate guests.

Backup orchestration and rehearsals must use a non-serving guest. Database
snapshot creation may remain on a serving node because it links existing files;
each node may archive its own local snapshot files. Backup capture must not read
the dataset row by row. Verification reads serve a separate purpose.

Bare `ops backup` refuses to perform a backup. Its error lists the registered
subcommands, and the command exits nonzero through the audited CLI entry point.

## Guest and storage identity

Configs prepares each guest and declares its IPv6 address, stable MAC address,
Docker IPv6 /96, neighbor-discovery proxy prefix, secrets, and schedules.
One designated guest runs initialization, migrations, and seeding. Select one
environment per operation and validate changes on QA before production.
Production applies, deployments, and destruction require explicit operator
authorization. Configuration must enter Configs before application; hand-edited
live configuration constitutes drift.

The current Configs [production workspace](https://github.com/agoodkind/configs/tree/main/opentofu/vault/tack-prod)
and [QA workspace](https://github.com/agoodkind/configs/tree/main/opentofu/suburban/tack-qa)
separate the environments. Each workspace assigns its guest identifiers explicitly.
An operator must read each identifier from the target environment. A production
identifier and an offset cannot determine the corresponding QA identifier.

Capacity approval must use current physical memory, configured guest memory,
and datastore free space. Guest memory limits impose ceilings rather than
reservations. A past capacity reading cannot establish current free capacity.
Destruction of a sandbox guest requires explicit confirmation of that exact
guest; a capacity shortfall does not authorize deletion.

Storage placement must protect ledger latency. The accepted layout assigns
ledger roots, product data, and app guests to NVMe. Object storage, rehearsal
scratch, export staging, and hypervisor guest backups use the SATA SSD. Broker
placement on the slower tier depends on removing synchronous broker work from
the request path. Unthrottled bulk writes on the ledger pool can delay log sync
and trigger leader elections. Live hypervisor readings must establish actual
placement.

## YugabyteDB nodes

Each of the three data guests must run a YugabyteDB node for authentication and
audit, a FoundationDB process for product data, and a Kafka broker for audit
events. YugabyteDB must keep three copies of every row.
Separate locations let the engine distinguish fault domains. App and operator
connection strings must list all three nodes. Callers must handle a query that fails during node
loss. A multi-host connection string does not retry an already failed query.

Each node must advertise and listen on its permanent name. Its own name must
resolve inside its container to an address it can bind. Peer names must resolve
inside peer containers to the owning guests' pinned addresses and published
ports. Guest-only hosts entries do not configure container name resolution.
Peer names require entries in each container's hosts file. Before joining a node,
a peer must resolve its name and connect to its published port. The isolated
bridge exposes only the ports that the deployment publishes.
Persisting a replaceable container address in the catalog can prevent a restart
from serving intact data after that address changes.

The base stack uses `yugabyte` as its stable local name; a distributed override
uses distinct names per node. Replacing a Compose command replaces the entire
command list. An override must preserve the tserver flagfile argument and the
patched startup overlay. Explicit process and cache limits must remain below
the container limit. Removing the flagfile can restore engine memory self-sizing
and allow a process to exhaust guest memory.

An online expansion must join nodes to the existing universe, pass through the
two-node state without delay, and establish full replication before retiring
the original node. Consumer connection strings must change before retirement.
Retirement must blacklist the old tablet server and wait until its load-move
metric reports 100 percent. A data node must assume the old node's master role
while the cluster maintains quorum. An external observer must confirm full
replication before retirement. A rename requires retirement and rejoin
while surviving nodes preserve the data; a single-node installation does not
provide that rolling rename procedure.

The IPv6-only bridge must remain IPv6-only. YugabyteDB image changes require a
matching overlay refresh and QA verification before production. Read the
[image-update procedure](../../runbooks/deployment/update-yugabyte-image.md)
before an image change.

## FoundationDB clients and agents

FoundationDB `double` redundancy stores two copies of each key. One coordinator
per data guest maintains a three-member coordinator set.
The client cluster directory must permit writes. Clients record coordinator
changes there without a restart. Startup must preserve an existing nonempty
cluster file; a new guest must start with the existing coordinator list rather
than initialize a separate cluster.

Each data guest publishes its FoundationDB port on its pinned IPv6 address.
Zone and machine identities distinguish guest fault domains. Cluster files must
use stable coordinator addresses. A positive transaction timeout bounds retries;
callers without a useful deadline must not retry forever. Backup agents cooperate
on the same continuous session, with one agent per data guest for survival.

## Broker and app requirements

The distributed broker contract requires three copies of the audit and
consumer-position topics, a two-live-copy minimum, and a routable advertised
address per broker. Kafka clients use bootstrap addresses to discover advertised
addresses; replacing only a bootstrap address cannot repair an unreachable
advertised listener. QA must prove the membership change for the deployed
metadata format. Online reassignment of both topics must limit bandwidth.

The current Kafka producer requires acknowledgments from every in-sync replica.
That setting differs from a promise that every configured replica remains
available. Producer delivery budgets must cover broker failure detection and
client metadata refresh. The default delivery budget is 15 seconds. The running
brokers must provide evidence of broker count and topic replication.

Two app guests must each run a stateless server. Both instances use the product
store transaction to detect duplicate writes across requests. Traefik checks
`GET /healthz` on each instance and selects only healthy instances. The handler
runs its datastore probes concurrently under one two-second deadline. It returns
HTTP 200 with `ok` when every probe succeeds and HTTP 503 with the name of a failing or
unfinished probe otherwise.

Only the audit consumer notarizes the audit chain. Each guest uses its own key.
Verification must reject a signer outside the environment's approved set, and
each notarization must record the signing host. The signer's own identifier
cannot establish trust or revocation.
Production authentication must require API tokens rather than accept arbitrary
user UUIDs.

## Provisioning and release boundaries

`ops provision` implements the ordered first-boot operation: guarded FoundationDB
initialization, migrations, audit-role setup, continuous-backup initialization
when enabled, and product seeding when configured. Initialization requires an
explicit fresh-environment option and must never run on production. Provisioning
uses Docker SDK calls from `tack-ops`; product seeding executes in the app
container. A deferred seed returns incomplete and requires provisioning again
after the app starts.

The current Configs [image-release module](https://github.com/agoodkind/configs/blob/main/opentofu/modules/tack-release/main.tf)
updates app and audit-consumer images and performs audited verification. It does
not prepare a different Compose topology, apply migrations, or create backup
timers. Its operator identity derives a UUIDv5 from the lowercased accountable
email in the operator namespace, and its audited checks include the service,
session, operator identifier, email, and deployed commit.

An image release cannot prove a topology migration, armed timers, or restorable
backups. The [acceptance criteria](2026-08-08-backup-rearch-acceptance.md)
require direct measurements of the running environment.
